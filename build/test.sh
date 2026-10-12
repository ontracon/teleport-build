#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
export OUT_DIR="${OUT_DIR:-/out}"
cd "${SOURCE_DIR:-/src/teleport}"
export CARGO_BUILD_JOBS="${BUILD_JOBS:-2}"
export GOMAXPROCS="${BUILD_JOBS:-2}"
export GOFLAGS="-p=${BUILD_JOBS:-2}"

rm -f "${OUT_DIR}/test-results/PASSED"
cd "${OUT_DIR}"
sha256sum --check SHA256SUMS
cd "${SOURCE_DIR:-/src/teleport}"

# Check the executables and fixture before compiling the race-enabled tests.
python3 "${script_dir}/smoke-test.py"

# Focus on server configuration, edition modules, and the bot configuration
# consumed by res-ansible-new. Use upstream's runner, tags, and JUnit reports.
libbpf_version="$(awk '$1 == "LIBBPF_VERSION" { print $3 }' build.assets/versions.mk)"
make --no-print-directory test-go-unit FIDO2=off \
  STATIC_LIBS="-L/usr/libbpf-${libbpf_version}/lib64 -lbpf -Wl,-Bdynamic -lelf -lz" \
  SUBJECT='./lib/config ./lib/modules ./lib/tbot/config' \
  FLAGS='-race -shuffle=on -count=1 -timeout=10m'

mkdir -p "${OUT_DIR}/test-results"
cp test-logs/unit-tests.xml test-logs/unit-tests.json "${OUT_DIR}/test-results/"
printf '%s\n' 'Upstream configuration tests and cluster smoke test passed.' \
  > "${OUT_DIR}/test-results/PASSED"
