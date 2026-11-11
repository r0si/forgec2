#!/bin/bash
# Verify that the public demo runs the image the canonical GHCR manifest
# points at.
#
#   bash deploy/demo/verify-image.sh [image-reference]
#
# The mirror that normally serves the demo has been answering 502 for new
# digests, in which case the image is pulled from GHCR by hand, loaded with
# `docker load` and runs under a local tag. Docker cannot resolve a digest it
# never pulled, so the pin is checked by CONTENT instead: the image ID is the
# config digest and the RootFS layers are the uncompressed layer digests, and
# together they commit to the whole filesystem of the image the canonical
# manifest names.
#
# The expected values live in image.lock next to this script; they were taken
# from the canonical manifest with
#   crane manifest/config ghcr.io/r0si/forgec2@<digest>
set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"
LOCK="$HERE/image.lock"
CONTAINER="${CONTAINER:-forgec2-demo}"
IMAGE="${1:-}"

[ -r "$LOCK" ] || { echo "missing $LOCK" >&2; exit 1; }

if [ -z "$IMAGE" ]; then
    IMAGE="$(docker inspect "$CONTAINER" --format '{{.Config.Image}}')"
fi

want_config="$(awk '$1 == "config" { print $2 }' "$LOCK")"
want_diff="$(awk '$1 == "diff_id" { print $2 }' "$LOCK")"
canonical="$(awk '$1 == "digest" { print $2 }' "$LOCK")"
have_config="$(docker image inspect "$IMAGE" --format '{{.Id}}')"
have_diff="$(docker image inspect "$IMAGE" --format '{{range .RootFS.Layers}}{{println .}}{{end}}')"
running="$(docker inspect "$CONTAINER" --format '{{.Image}}')"

fail=0
if [ "$want_config" != "$have_config" ]; then
    echo "FAIL config digest: want $want_config, have $have_config"
    fail=1
fi
if [ "$want_diff" != "$have_diff" ]; then
    echo "FAIL the layer diff ids differ from $LOCK"
    fail=1
fi
if [ "$running" != "$have_config" ]; then
    echo "FAIL the container runs $running, not $have_config"
    fail=1
fi
if [ "$fail" -ne 0 ]; then
    exit 1
fi

echo "OK: $IMAGE is content-identical to the canonical manifest $canonical"
