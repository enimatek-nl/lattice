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
echo "== running Complement (filter: $filter, packages in parallel: $parallel)"
cd "$complement_dir"
COMPLEMENT_BASE_IMAGE="$image" go test ./tests/... -run "$filter" -count=1 -p "$parallel" "${@:1}"
