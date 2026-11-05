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
complement_dir="${COMPLEMENT_DIR:-../complement}"

if [[ ! -f "$complement_dir/go.mod" ]]; then
	cat >&2 <<-EOF
	Complement checkout not found at: $complement_dir
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

echo "== running Complement (filter: $filter)"
cd "$complement_dir"
COMPLEMENT_BASE_IMAGE="$image" go test ./tests/... -run "$filter" -count=1 "${@:1}"
