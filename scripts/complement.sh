#!/usr/bin/env bash
# Build the Lattice Complement image and run Complement's suites against it.
#
# Usage:
#   scripts/complement.sh                       # the M1-relevant suites
#   RUN=TestLogin scripts/complement.sh         # a filter
#   COMPLEMENT_DIR=~/complement scripts/complement.sh -run TestRegistration -v
#
# Complement runs homeservers as containers, so this needs a working Docker or
# Podman daemon. Nothing here weakens a Complement assertion: failures are
# reported as failures.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

image="${COMPLEMENT_BASE_IMAGE:-complement-lattice:latest}"
# Candidate locations for the Complement checkout, in the order people usually have it.
candidates=("${COMPLEMENT_DIR:-}" ../complement "$HOME/complement" ../complement-checkout)
complement_dir=""
for candidate in "${candidates[@]}"; do
	if [[ -n "$candidate" && -f "$candidate/go.mod" ]]; then
		complement_dir="$candidate"
		break
	fi
done

if [[ -z "$complement_dir" ]]; then
	cat >&2 <<-EOF
	Complement checkout not found. Looked in: ${candidates[*]}
	Clone it, or point COMPLEMENT_DIR at an existing checkout:
	    git clone https://github.com/matrix-org/complement ../complement
	EOF
	exit 1
fi

container_runtime="${CONTAINER_RUNTIME:-docker}"
if ! command -v "$container_runtime" >/dev/null 2>&1; then
	echo "container runtime '$container_runtime' not found" >&2
	exit 1
fi

echo "== building $image"
"$container_runtime" build -t "$image" -f deploy/complement/Dockerfile .

# Podman's container network reaches the host under a different name; Complement
# needs to know which one to advertise to homeservers.
if "$container_runtime" --version 2>&1 | grep -qi podman; then
	export COMPLEMENT_HOSTNAME_RUNNING_COMPLEMENT="${COMPLEMENT_HOSTNAME_RUNNING_COMPLEMENT:-host.containers.internal}"
fi

filter="${RUN:-TestVersionStructure|TestUnknownEndpoints|TestRegistration|TestLogin|TestLogout|TestServerCapabilities|TestDeviceManagement|TestCanRegisterAdmin}"

# A Complement run creates a container and an anonymous volume per homeserver, and volumes
# of removed containers are left behind. Past a few hundred of them Podman refuses new work —
# `allocating lock for new volume: allocation failed; exceeded num_locks (2048)` — and Complement
# reports that as test failures at deploy time, with sub-second durations. On 2026-11-06 a full
# suite showed 59 passes and 161 failures, and 15+ sampled failures were that error; 2044 leaked
# anonymous volumes were the whole of it, most of them from one run interrupted with pkill.
# So: the count is reported, and the way to clear it is named rather than run automatically,
# because deleting volumes on someone else's machine is not this script's call:
#
#   keep=$(for c in $(podman ps -aq); do podman inspect "$c" --format '{{range .Mounts}}{{.Name}} {{end}}'; done)
#   for v in $(podman volume ls -q); do case "$v" in *[!0-9a-f]*|"") continue;; esac
#     [ ${#v} -eq 64 ] || continue; case "$keep" in *"$v"*) continue;; esac
#     podman volume rm "$v" >/dev/null; done
#
# Packages are also run a few at a time, which keeps a single run from creating hundreds at once.
parallel="${PARALLEL:-2}"
if command -v podman >/dev/null 2>&1; then
	volumes="$(podman volume ls --format '{{.Name}}' 2>/dev/null | grep -cE '^[0-9a-f]{64}$' || true)"
	# A count over 300 is worth saying out loud. A count within one suite's reach of podman's lock
	# table (2048) is worth refusing over: on 2026-11-07 a full suite started with 2038 volumes and
	# came out "3 passing, 217 failing" because 63 homeservers never started. That number measures
	# the runtime, not the server, and a run that cannot deploy is not a tally of the protocol. A
	# suite of this size costs roughly 700 volumes, so the threshold is a suite and a half away.
	if [[ "${volumes:-0}" -gt 300 ]]; then
		echo "!! $volumes anonymous volumes in the podman store; deploy failures below may be lock exhaustion, not test failures (see the comment above for how to clear them)"
	fi
	if [[ "${volumes:-0}" -gt 1300 && "${COMPLEMENT_FORCE:-}" != 1 ]]; then
		cat >&2 <<-EOF
		!! REFUSING TO START: $volumes anonymous volumes, and podman's lock table holds 2048.
		!! A suite this size adds about 700, so most deployments would fail and the run would
		!! report the runtime rather than the server — see run 20261107l in docs/compliance-matrix.md.
		!! Clear the store first: the loops are in this script's comment above. They delete state
		!! that may belong to other projects, so they stay an operator's call. Re-run afterwards, or
		!! set COMPLEMENT_FORCE=1 to run anyway and read the result as unfinished.
		EOF
		exit 1
	fi
fi
# The other way a Podman run lies about the protocol: stale per-network DNS.
#
# aardvark-dns serves one Podman network's name records from one file in the runtime directory, and
# a network that has been removed can leave its file behind. The records in it keep being answered,
# so a homeserver container asking where `hs1` is can be pointed at an address in a subnet that
# belonged to a deleted deployment — a different test's hs2, or nothing at all. What that looks like
# in a run is not a DNS error: it is `certificate is valid for hs2, not hs1`, a 404 for a room the
# same deployment created seconds earlier, and a restricted-room join that fails over into a server
# that has never heard of the room. On 2026-11-06 this made about half the runs of
# `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room` fail at a 5 second sync wait, and the file
# behind it was `…/containers/networks/aardvark-dns/complement_msc3902_1_servers` naming 10.89.12.2
# for `hs1` while the live hs1 was 10.89.0.4. `podman network prune` does not clear it: the network
# is already gone, so there is nothing to prune.
#
# Again the count is reported and the fix named rather than run: this touches another project's
# running containers on someone else's machine. Clearing it is two commands, and the second one is
# safe because Podman starts a fresh aardvark-dns when a network needs one:
#
#   dns="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/containers/networks/aardvark-dns"
#   for f in "$dns"/*; do n=${f##*/}; [ "$n" = aardvark.pid ] && continue
#     podman network exists "$n" 2>/dev/null || rm -f "$f"; done
#   kill "$(cat "$dns/aardvark.pid")" 2>/dev/null || true
if command -v podman >/dev/null 2>&1; then
	dns_dir="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/containers/networks/aardvark-dns"
	if [[ -d "$dns_dir" ]]; then
		# A subshell so nullglob does not leak into the rest of the script. It is needed because a
		# directory holding only aardvark.pid has nothing to report, and an unexpanded glob would be
		# reported as a network named "*".
		(
			shopt -s nullglob
			stale=""
			for entry in "$dns_dir"/*; do
				name="${entry##*/}"
				[[ "$name" == *.pid ]] && continue
				podman network exists "$name" 2>/dev/null || stale="$stale $name"
			done
			if [[ -n "$stale" ]]; then
				echo "!! podman DNS records for networks that no longer exist:$stale — a peer server name can resolve to an address from a deleted deployment, which reads as a TLS or protocol failure (see the comment above for how to clear it)"
			fi
		)
	fi
fi

# The run, its log, and its tally.
#
# Two recorded tallies in this project's own history drifted from the logs they were
# written from — a test described as "newly passing" while the run's list of the same
# date still had it failing, and a device-list test marked "verified, all ten subtests"
# in prose that its own failures list contradicted (and that fails on a clean re-run).
# The cure is not more care; it is that the lists and the summary line a tally is quoted
# from come out of the same run as the log, so quoting a number without its log is
# impossible. So: the raw output always goes to a log under .agent-work/next/, and the
# pass and failure lists — top-level tests, not subtests, which is the universe every
# tally in docs/compliance-matrix.md names — are cut from that log.
#
# COMPLEMENT_LABEL names the artefacts: with COMPLEMENT_LABEL=20261107a this writes
# .agent-work/complement-{passes,failures}-20261107a.txt alongside the log.
#
# go test's default per-package budget is 10 minutes, and the whole-suite run needs
# more than that on a loaded machine. A package that blows its budget does not report
# failures: it panics, prints a goroutine dump, and leaves every test after the one it
# reached unrun — which reads as "these tests fail" and is really "these tests never
# ran". COMPLEMENT_TEST_TIMEOUT sets the budget; a run that hits it is reported as an
# unfinished run rather than a tally.
timeout="${COMPLEMENT_TEST_TIMEOUT:-30m}"
label="${COMPLEMENT_LABEL:-$(date -u +%Y%m%dT%H%M%SZ)}"
run_log="$repo_root/.agent-work/next/complement-run-$label.log"
results="$repo_root/.agent-work/next/complement-results-$label.tsv"
mkdir -p "$repo_root/.agent-work/next"

# Top-level results only. Subtests are counted inside their parent, exactly the way the
# compliance matrix counts them, so a list cut here and a tally quoted there are the
# same arithmetic.
passes="$repo_root/.agent-work/complement-passes-$label.txt"
failures="$repo_root/.agent-work/complement-failures-$label.txt"

cd "$complement_dir"
exit_code=0
if command -v jq >/dev/null 2>&1 && [[ "${COMPLEMENT_NO_JSON:-}" != 1 ]]; then
	# `go test -json`, so every result carries its package. Two packages can hold the same
	# test name — `TestSync` exists in `csapi` and in `tests/msc4222` — and a list of bare
	# names then silently reports one result for two tests, or one failure as if a passing
	# test had failed. The qualified table is written alongside the bare lists the compliance
	# matrix cites; the log is rendered with the package on each line, which is also what
	# makes a parallel run readable at all.
	echo "== running Complement (filter: $filter, packages in parallel: $parallel, per-package timeout: $timeout)"
	echo "== raw output: $run_log (go test -json); qualified table: $results"
	COMPLEMENT_BASE_IMAGE="$image" go test ./tests/... -run "$filter" -count=1 -p "$parallel" \
		-timeout "$timeout" "${@:1}" -json 2>&1 |
		tee "$run_log" |
		jq --unbuffered -r 'select(.Action=="output") | ((.Package // "-") | sub("^github\\.com/matrix-org/complement/?"; "")) + " | " + .Output' || exit_code=$?

	jq -r 'select(.Test != null and (.Action=="pass" or .Action=="fail" or .Action=="skip") and ((.Test | contains("/")) | not)) |
		"\(.Action)\t\((.Package // "-") | sub("^github\\.com/matrix-org/complement/?"; ""))\t\(.Test)"' "$run_log" |
		sort -u > "$results"
	awk -F'\t' '$1=="pass"{print $3 > "'"$passes"'"} $1=="fail"{print $3 > "'"$failures"'"}' "$results"
else
	echo "!! jq not found: falling back to plain output, so the pass/failure lists carry no package name and a test name shared by two packages is counted once."
	echo "== running Complement (filter: $filter, packages in parallel: $parallel, per-package timeout: $timeout)"
	echo "== raw output: $run_log"
	COMPLEMENT_BASE_IMAGE="$image" go test ./tests/... -run "$filter" -count=1 -p "$parallel" \
		-timeout "$timeout" "${@:1}" 2>&1 | tee "$run_log" || exit_code=$?
	grep -E '^--- (PASS|FAIL|SKIP): ' "$run_log" | sed -E 's/^--- ([A-Z]+): ([^ ]+).*/\1 \2/' | sort -u |
		awk '$1=="PASS"{print $2 > "'"$passes"'"} $1=="FAIL"{print $2 > "'"$failures"'"}'
fi
[[ -f "$passes" ]] || : > "$passes"
[[ -f "$failures" ]] || : > "$failures"

passed=$(wc -l < "$passes" | tr -d ' ')
failed=$(wc -l < "$failures" | tr -d ' ')
if [[ -f "$results" ]]; then
	skipped=$(awk -F'\t' '$1=="skip"' "$results" | wc -l | tr -d ' ')
else
	skipped=$(grep -cE '^--- SKIP: ' "$run_log" || true)
fi
echo "== tally: $passed passing, $failed failing, $skipped skipped (top-level tests)"
echo "== lists: $passes, $failures"

# Neither a tally nor an unfinished one: nothing was derived at all. A broken renderer or a
# `go test` that died before running anything looks, from the tally line alone, like a suite
# of zero tests.
if [[ "$((passed + failed + skipped))" -eq 0 && "$exit_code" -ne 0 ]]; then
	echo "!! NO RESULTS DERIVED and go test exited $exit_code: nothing above is a tally. Check the renderer (jq) and the top of $run_log for a build or flag error."
fi

# A name in both lists is not one test that both passed and failed: it is two tests with
# the same name in two packages. Say which, because otherwise the pair reads as a flake.
both=$(comm -12 <(sort "$passes") <(sort "$failures"))
if [[ -n "$both" ]]; then
	echo "!! these names appear in both lists, because more than one Complement package defines them; each line below is one package's result, and the bare-name tally counts them once:"
	echo "$both" | while read -r name; do awk -F'\t' -v n="$name" '$3==n{print "   " $1 "  " $2}' "$results"; done
fi

# A killed package is not a smaller suite; it is an unfinished one. Say so loudly, next
# to the tally, because a tally printed on its own invites exactly the wrong reading.
if grep -q 'panic: test timed out' "$run_log"; then
	packages=$(grep -E 'panic: test timed out after' "$run_log" | sed -E 's/.*after ([^ ]+).*/\1/' | tr '\n' ' ')
	echo "!! THIS RUN IS INCOMPLETE: a package hit its per-package timeout ($packages). Tests after the one it reached never ran; the numbers above are a floor, not a tally. Raise COMPLEMENT_TEST_TIMEOUT before quoting them."
fi
if grep -qE 'failed to deployBaseImage|timed out checking for homeserver to be up' "$run_log"; then
	echo "!! some deployments never started. Those failures are the container or the environment, not the protocol: check the image starts at all (podman run --rm <image>) and the volume/DNS warnings above."
fi
exit "$exit_code"
