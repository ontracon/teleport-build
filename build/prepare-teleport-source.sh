#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
source_dir=''
adapter_dir="${repo_dir}/oidc-adapter"
integration_dir="${repo_dir}/integration"
check_only=false

usage() {
  printf '%s\n' \
    'Usage: prepare-teleport-source.sh --source CHECKOUT [--check]' \
    '       [--adapter DIRECTORY] [--integration DIRECTORY]' \
    'Requires a fresh checkout of the commit in integration/upstream.commit.'
}

fail() {
  printf 'Source preparation failed: %s\n' "$*" >&2
  exit 1
}

while (($#)); do
  case "$1" in
    --source|--adapter|--integration)
      (($# >= 2)) || fail "missing value for $1"
      case "$1" in
        --source) source_dir="$2" ;;
        --adapter) adapter_dir="$2" ;;
        --integration) integration_dir="$2" ;;
      esac
      shift 2
      ;;
    --check) check_only=true; shift ;;
    --help|-h) usage; exit 0 ;;
    *) usage >&2; fail "unknown argument: $1" ;;
  esac
done

[[ -n "$source_dir" ]] || { usage >&2; fail '--source is required'; }
source_dir="$(cd -- "$source_dir" && pwd -P)"
adapter_dir="$(cd -- "$adapter_dir" && pwd -P)"
integration_dir="$(cd -- "$integration_dir" && pwd -P)"
[[ "$(git -C "$source_dir" rev-parse --show-toplevel)" == "$source_dir" ]] || fail '--source must be the checkout root'

expected_commit="$(cat "${integration_dir}/upstream.commit")"
[[ "$expected_commit" =~ ^[0-9a-f]{40}$ ]] || fail 'invalid integration/upstream.commit'
actual_commit="$(git -C "$source_dir" rev-parse HEAD)"
[[ "$actual_commit" == "$expected_commit" ]] || fail "expected upstream commit $expected_commit, found $actual_commit"

# diff refreshes unchanged stat entries restored from Docker layers. These
# checks also ensure preparation cannot obscure tracked developer changes.
git -C "$source_dir" diff --quiet || fail 'checkout has tracked local edits; use a fresh checkout'
git -C "$source_dir" diff --cached --quiet || fail 'checkout has staged edits; use a fresh checkout'

patches=()
while IFS= read -r name || [[ -n "$name" ]]; do
  case "$name" in ''|\#*) continue ;; esac
  [[ "$name" =~ ^[0-9]{4}-[a-z0-9-]+[.]patch$ ]] || fail "invalid patch name in series: $name"
  [[ -f "${integration_dir}/patches/${name}" ]] || fail "missing patch: $name"
  patches+=("${integration_dir}/patches/${name}")
done < "${integration_dir}/patches/series"
((${#patches[@]} > 0)) || fail 'patch series is empty'

shopt -s nullglob
adapter_files=("${adapter_dir}"/*.go)
((${#adapter_files[@]} > 0)) || fail 'adapter source is missing'
[[ -f "${adapter_dir}/config.example.json" ]] || fail 'adapter example configuration is missing'
[[ -d "${integration_dir}/overlay" ]] || fail 'integration overlay is missing'

# Preflight every change before applying patches or installing new files.
[[ ! -e "${source_dir}/lib/zitadeloidc" && ! -L "${source_dir}/lib/zitadeloidc" ]] || fail 'adapter destination exists; use a fresh checkout'
while IFS= read -r -d '' file; do
  relative="${file#"${integration_dir}/overlay/"}"
  if git -C "$source_dir" ls-files --error-unmatch -- "$relative" >/dev/null 2>&1; then
    fail "overlay file is tracked upstream; maintain it as a patch: $relative"
  fi
  [[ ! -e "${source_dir}/${relative}" && ! -L "${source_dir}/${relative}" ]] || fail "overlay destination exists: $relative"
done < <(find "${integration_dir}/overlay" -type f -print0)
git -C "$source_dir" apply --check --index --whitespace=error-all "${patches[@]}"

if "$check_only"; then
  printf 'Verified %s patches and new-file destinations at %s.\n' "${#patches[@]}" "$expected_commit"
  exit 0
fi

git -C "$source_dir" apply --whitespace=error-all "${patches[@]}"
mkdir -p "${source_dir}/lib/zitadeloidc"
cp "${adapter_files[@]}" "${adapter_dir}/config.example.json" "${source_dir}/lib/zitadeloidc/"
cp -R "${integration_dir}/overlay/." "$source_dir/"
printf 'Prepared Teleport at %s with %s patches and the Zitadel integration.\n' "$expected_commit" "${#patches[@]}"
