#!/usr/bin/env bash
# Publish the already-tested image, without recompiling or rebuilding its layers.
set -euo pipefail

: "${TELEPORT_VERSION:?}"
: "${RELEASE_BUILD:?}"
: "${GITHUB_REPOSITORY:?}"
: "${GITHUB_SHA:?}"
: "${RUNTIME_IMAGE_ID:?}"
: "${GITHUB_OUTPUT:?}"

[[ "$RELEASE_BUILD" =~ ^[0-9]+\.[0-9]+$ ]] || { echo 'Invalid release build' >&2; exit 1; }
repository="ghcr.io/$(printf '%s' "$GITHUB_REPOSITORY" | tr '[:upper:]' '[:lower:]')"
[[ "$repository" =~ ^ghcr.io/[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9_.-]*$ ]] || {
  echo 'Invalid GHCR repository' >&2; exit 1;
}
# OCI tags cannot contain '+'. Keep the run/attempt suffix unique in either case.
tag="${TELEPORT_VERSION//+/-}-build.${RELEASE_BUILD}"
[[ "$tag" =~ ^[0-9][a-zA-Z0-9_.-]{0,127}$ ]] || { echo 'Invalid image tag' >&2; exit 1; }
local_image="teleport-zitadel:${TELEPORT_VERSION//+/-}"
image="${repository}:${tag}"

actual_id="$(docker image inspect --format '{{.Id}}' "$local_image")"
[[ "$actual_id" == "$RUNTIME_IMAGE_ID" ]] || { echo 'Loaded image differs from the tested image' >&2; exit 1; }
revision="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$local_image")"
[[ "$revision" == "$GITHUB_SHA" ]] || { echo 'Image source revision does not match release' >&2; exit 1; }
version="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$local_image")"
[[ "$version" == "$TELEPORT_VERSION" ]] || { echo 'Image version does not match release' >&2; exit 1; }
platform="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$local_image")"
[[ "$platform" == linux/amd64 ]] || { echo 'Expected a tested Linux amd64 image' >&2; exit 1; }

docker tag "$local_image" "$image"
docker push "$image"
# The Docker exporter produces a single-platform manifest. Inspect it remotely
# using Docker's built-in manifest command (no Buildx config on this runner needed).
digest="$(docker manifest inspect --verbose "$image" | python3 -c 'import json, sys; print(json.load(sys.stdin)["Descriptor"]["digest"])')"
[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo 'Registry returned an invalid image digest' >&2; exit 1; }
{
  printf 'image=%s\n' "$image"
  printf 'digest=%s\n' "$digest"
} >> "$GITHUB_OUTPUT"
printf 'Published %s@%s\n' "$repository" "$digest"
