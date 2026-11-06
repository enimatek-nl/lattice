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
	[[ "${volumes:-0}" -gt 300 ]] && echo "!! $volumes anonymous volumes in the podman store; deploy failures below may be lock exhaustion, not test failures (see the comment above for how to clear them)"
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

echo "== running Complement (filter: $filter, packages in parallel: $parallel)"
cd "$complement_dir"
COMPLEMENT_BASE_IMAGE="$image" go test ./tests/... -run "$filter" -count=1 -p "$parallel" "${@:1}"
