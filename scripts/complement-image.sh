#!/usr/bin/env bash
# Build the homeserver image the Complement harness runs, and record which tree went into it.
#
# Two things this settles that the runner used to leave open:
#
#   Time.  The image was compiled inside the build, from a layer cache that a source change
#   invalidates, so every Complement iteration paid ~45s of wall clock and ~3min of CPU for a
#   binary the host cross-compiles in ~2s. The compile happens here, once, on the host cache.
#
#   Provenance.  A tally quoted against a commit is only worth what the image under test is worth.
#   Run 20261108gg printed a tree that was the commit *before* the code it was testing, because the
#   image had been built from a dirty tree and nothing in the run said so. So the tree identity is
#   written into the image at /REVISION, the image is tagged with the revision, and this script then
#   asks the image — not the caller — what it contains, and fails if the answer is not the tree it
#   was told to build.
#
# Usage:
#   scripts/complement-image.sh                      # host build, tag :latest and :<rev>
#   BUILD_MODE=container scripts/complement-image.sh # the reproducible in-build compile (Dockerfile)
#   IMAGE=complement-lattice:latest scripts/complement-image.sh
#
# Exit status is non-zero if the image does not report the tree it was built from.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

image="${IMAGE:-${COMPLEMENT_BASE_IMAGE:-complement-lattice:latest}}"
mode="${BUILD_MODE:-host}"
container_runtime="${CONTAINER_RUNTIME:-docker}"

if ! command -v "$container_runtime" >/dev/null 2>&1; then
	echo "container runtime '$container_runtime' not found" >&2
	exit 1
fi

# The tree. `git status --porcelain` skips ignored paths, so the working notes and run evidence
# under .agent-work do not make a tree dirty.
rev="$(git -C "$repo_root" rev-parse --short HEAD 2>/dev/null || echo unknown)"
dirty="$(git -C "$repo_root" status --porcelain 2>/dev/null | grep -v '^?? \.complement-build/' | head -50 || true)"
tree_state=clean
[[ -n "$dirty" ]] && tree_state="dirty"

staging="$repo_root/.complement-build"
mkdir -p "$staging"

# The revision file, written before the build so that it can only describe the tree being built.
revision_file="$staging/REVISION"
{
	echo "revision=$rev"
	echo "tree=$tree_state"
	echo "built=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	echo "mode=$mode"
	if [[ "$mode" == host ]]; then
		echo "compiler=$(go env GOOS)/$(go env GOARCH) $(go version | sed 's/^go version //')"
	else
		echo "compiler=in-build (deploy/complement/Dockerfile)"
	fi
} > "$revision_file"

case "$mode" in
host)
	# Cross-compiled statically so the runtime stage stays a plain Alpine and the result is the
	# same binary the release build produces.
	echo "== building the binary on the host ($(go env GOOS)/$(go env GOARCH), revision $rev, $tree_state))"
	CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH:-$(go env GOARCH)}" \
		go build -trimpath -ldflags="-s -w" -o "$staging/lattice" ./cmd/lattice

	echo "== assembling $image from the prebuilt binary"
	"$container_runtime" build \
		-t "$image" \
		-t "${image%:*}:$rev" \
		-f deploy/complement/Dockerfile.prebuilt "$repo_root"
	;;
container)
	# The reproducible path: everything compiled inside the build from the repository alone.
	# Slower, and the point of keeping it is that it stays true.
	echo "== building $image inside the build (revision $rev, $tree_state)"
	"$container_runtime" build \
		--build-arg REVISION="$rev" --build-arg TREE="$tree_state" \
		-t "$image" -t "${image%:*}:$rev" \
		-f deploy/complement/Dockerfile "$repo_root"
	;;
*)
	echo "BUILD_MODE must be 'host' or 'container', got '$mode'" >&2
	exit 2
	;;
esac

# Ask the image what it says it is. A build that could not carry /REVISION is a build whose
# provenance cannot be checked, which is the thing this exists to prevent.
echo "== verifying the image reports its own origin"
reported="$("$container_runtime" run --rm --entrypoint cat "$image" /REVISION 2>/dev/null || true)"
if [[ -z "$reported" ]]; then
	echo "!! the image carries no /REVISION: its provenance cannot be checked, so a tally against $rev would be unsupported" >&2
	exit 1
fi
image_rev="$(printf '%s\n' "$reported" | sed -n 's/^revision=//p')"
if [[ "$image_rev" != "$rev" ]]; then
	echo "!! the image reports revision=$image_rev but the tree is $rev — this image did not come from this tree" >&2
	printf '%s\n' "$reported" >&2
	exit 1
fi
printf '%s\n' "$reported" | sed 's/^/   /'
echo "== $image (and ${image%:*}:$rev) built from $rev, tree $tree_state"

# Tell the runner what it is looking at, so the summary next to a tally can carry it without
# repeating the container call.
printf 'IMAGE_REVISION=%s\nIMAGE_TREE=%s\nIMAGE_MODE=%s\n' "$image_rev" "$tree_state" "$mode" \
	> "$staging/image.env"

if [[ "$tree_state" == dirty ]]; then
	cat >&2 <<-EOF
	!! building from a dirty tree: the run can be reproduced only by the working copy, not by $rev.
	!! Changed since that commit:
	$(printf '%s\n' "$dirty" | sed 's/^/     /')
	EOF
fi
