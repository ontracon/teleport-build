#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
export OUT_DIR="${OUT_DIR:-/out}"
cd "${SOURCE_DIR:-/src/teleport}"
export CARGO_BUILD_JOBS="${BUILD_JOBS:-2}"
export GOMAXPROCS="${BUILD_JOBS:-2}"
export GOFLAGS="-p=${BUILD_JOBS:-2}"

results_dir="${OUT_DIR}/test-results"
mkdir -p "$results_dir"
rm -f "${results_dir}/PASSED"
go_cache="$(go env GOCACHE)"
temporary_dir="${GOTMPDIR:-${TMPDIR:-/tmp}}"

report_disk() {
  {
    printf '\nDisk usage: %s\n' "$1"
    df -h . "$temporary_dir" "$go_cache" "$OUT_DIR" || true
    df -i . "$temporary_dir" "$go_cache" "$OUT_DIR" || true
  } | tee -a "${results_dir}/disk-usage.log" || true
}

finish() {
  local status=$?
  trap - EXIT
  set +e
  report_disk "verification finished (exit ${status})"
  if ((status != 0)); then
    du -sh "$go_cache" "$temporary_dir" /go/pkg/mod /src/teleport/target 2>&1 |
      tee -a "${results_dir}/disk-usage.log"
  fi
  exit "$status"
}
trap finish EXIT

report_disk 'before verification'
cd "${OUT_DIR}"
sha256sum --check SHA256SUMS
cd "${SOURCE_DIR:-/src/teleport}"

# Check the executables and fixture before compiling the race-enabled tests.
python3 "${script_dir}/smoke-test.py"

# All Go suites use the same upstream build tags, avoiding another set of
# race-instrumented dependencies for the integration tests. Link test binaries
# serially and omit DWARF to reduce peak temporary disk use. Race detection
# and Go's runtime stack traces remain enabled.
libbpf_version="$(awk '$1 == "LIBBPF_VERSION" { print $3 }' build.assets/versions.mk)"
run_go_suite() {
  local name="$1" subject="$2" pattern="${3:-}"
  local flags='-race -shuffle=on -count=1 -p=1 -timeout=10m -ldflags=-w'
  if [[ -n "$pattern" ]]; then
    # Make expands FLAGS into a shell command; keep regex metacharacters literal.
    printf -v flags '%s -run=%q' "$flags" "$pattern"
  fi
  report_disk "before ${name}"
  make --no-print-directory test-go-unit FIDO2=off \
    STATIC_LIBS="-L/usr/libbpf-${libbpf_version}/lib64 -lbpf -Wl,-Bdynamic -lelf -lz" \
    SUBJECT="$subject" FLAGS="$flags" TEST_LOG_DIR="${results_dir}/${name}"
}

run_go_suite upstream './lib/config ./lib/modules ./lib/tbot/config'
run_go_suite zitadel-adapter './lib/zitadeloidc'
run_go_suite auth-bridge './lib/auth' '^TestZitadel'
run_go_suite account-labels './lib/web/ui' '^TestZitadel|^TestNewUserContext'
run_go_suite proxy-bridge './lib/web' '^TestZitadel'
run_go_suite plugin './lib/service' '^TestZitadel'

pnpm exec jest --runInBand --runTestsByPath \
  web/packages/teleport/src/services/user/zitadel-display-name.test.ts \
  web/packages/teleport/src/components/UserMenuNav/UserMenuNav.test.tsx

printf '%s\n' 'Cluster smoke, upstream, Zitadel integration, and Web UI tests passed.' \
  > "${results_dir}/PASSED"
