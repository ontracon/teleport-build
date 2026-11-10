#!/usr/bin/env bash
# Build two real verified versions, retain both outputs, and load the newer lab image.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${TELEPORT_VERSION:=18.10.0}"
: "${LAB_START_BUILD:=10.1}"
: "${LAB_TARGET_BUILD:=11.1}"
: "${BUILD_PLATFORM:=linux/amd64}"
export TELEPORT_VERSION BUILD_PLATFORM
for build in "$LAB_START_BUILD" "$LAB_TARGET_BUILD"; do
  [[ "$build" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "Invalid build: $build" >&2; exit 1; }
done
[[ "$LAB_START_BUILD" != "$LAB_TARGET_BUILD" ]] || { echo 'Lab requires two distinct build versions.' >&2; exit 1; }
python3 dev/mirror-lab/setup.py
ensure_artifacts() {
  local build="$1" destination="dist/mirror-lab/$1"
  if [[ -e "$destination" ]]; then
    # Versioned mirror names are immutable. Reuse only artifacts that still
    # match the pinned source, integration files, binary hashes and tests.
    TELEPORT_BUILD_NUMBER="$build" TELEPORT_COMMIT="$(cat integration/upstream.commit)" \
      python3 build/verify-release.py "$destination" || {
        echo "Existing artifacts at $destination do not match; choose a new build number." >&2
        return 1
      }
  else
    TELEPORT_BUILD_NUMBER="$build" docker buildx bake --file build/docker-bake.hcl \
      --set "artifacts.output=type=local,dest=${destination}" artifacts
  fi
}
ensure_artifacts "$LAB_START_BUILD"
ensure_artifacts "$LAB_TARGET_BUILD"
docker buildx build --platform "$BUILD_PLATFORM" --load \
  --tag "teleport-mirror-lab:${TELEPORT_VERSION}-${LAB_TARGET_BUILD}" \
  --file dev/mirror-lab/cluster.Dockerfile "dist/mirror-lab/${LAB_TARGET_BUILD}"
python3 dev/mirror-lab/publish.py "dist/mirror-lab/${LAB_START_BUILD}" "dist/mirror-lab/${LAB_TARGET_BUILD}"
printf 'Built and published %s-%s and %s-%s.\n' "$TELEPORT_VERSION" "$LAB_START_BUILD" "$TELEPORT_VERSION" "$LAB_TARGET_BUILD"
