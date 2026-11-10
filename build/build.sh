#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
export OUT_DIR="${OUT_DIR:-/out}"
cd "${SOURCE_DIR:-/src/teleport}"
case "${BUILD_JOBS:-2}" in
  ''|*[!0-9]*|0) echo 'BUILD_JOBS must be a positive integer' >&2; exit 1 ;;
esac
export CARGO_BUILD_JOBS="${BUILD_JOBS:-2}"
export GOMAXPROCS="${BUILD_JOBS:-2}"
export GOFLAGS="-p=${BUILD_JOBS:-2}"

# The upstream full target builds the UI, Rust Desktop libraries, all six
# executables, and the Linux session helper embedded in teleport.
# Cargo/Go perform their own parallel compilation. Keep Make's dependency
# installation steps serial so tool installations do not race each other.
# Keep libbpf static and use the host's shared libelf/zlib libraries, then
# switch to shared linkage for its system dependencies.
libbpf_version="$(awk '$1 == "LIBBPF_VERSION" { print $3 }' build.assets/versions.mk)"
distribution_version="${TELEPORT_VERSION}"
if [[ -n "${TELEPORT_BUILD_NUMBER:-}" ]]; then
  distribution_version="$(python3 "${script_dir}/stamp-version.py" --source . \
    --upstream "${TELEPORT_VERSION}" --build "${TELEPORT_BUILD_NUMBER}")"
fi
export TELEPORT_DISTRIBUTION_VERSION="${distribution_version}"
# The upstream Rust build script writes this header into the source tree.
# A fresh checkout combined with a warm Cargo target cache can skip that build
# script, leaving cgo without its required header. Invalidate only this crate
# when the generated file is missing; retain the dependency cache.
if [[ ! -f lib/srv/desktop/rdp/rdpclient/librdpclient.h ]]; then
  rust_target="$(rustc -vV | awk '$1 == "host:" { print $2 }')"
  cargo clean -p rdp-client --release --target "${rust_target}"
fi
make --no-print-directory full FIDO2=off \
  VERSION="${distribution_version}" \
  STATIC_LIBS="-L/usr/libbpf-${libbpf_version}/lib64 -lbpf -Wl,-Bdynamic -lelf -lz"
mkdir -p "${OUT_DIR}/bin"
for binary in teleport tctl tsh tbot fdpass-teleport teleport-update; do
  install -m 0755 "build/${binary}" "${OUT_DIR}/bin/${binary}"
done
cp LICENSE "${OUT_DIR}/LICENSE"

python3 "${script_dir}/verify-build.py"
