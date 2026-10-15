#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
[[ $# == 1 && -n "$1" ]] || { printf 'Usage: %s UPSTREAM_CHECKOUT\n' "$0" >&2; exit 1; }
upstream="$(cd -- "$1" && pwd -P)"
prepare="${repo_dir}/build/prepare-teleport-source.sh"
integration="${repo_dir}/integration"
commit="$(cat "${integration}/upstream.commit")"
mkdir -p "${repo_dir}/tests/build/test-results"
results="$(mktemp -d "${repo_dir}/tests/build/test-results/prepare.XXXXXX")"
source_dir="${results}/source"

fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
expect_failure() {
  local name="$1"
  shift
  if "$@" >"${results}/${name}.log" 2>&1; then
    fail "$name unexpectedly succeeded"
  fi
}
assert_unmodified() {
  git -C "$source_dir" diff --quiet || fail 'tracked source changed during preflight'
  git -C "$source_dir" diff --cached --quiet || fail 'index changed during preflight'
  [[ ! -e "${source_dir}/lib/zitadeloidc" ]] || fail 'adapter installed during preflight'
}

# A local sparse clone preserves the actual upstream commit without touching
# the supplied checkout or fetching anything from the network.
git clone --quiet --shared --no-checkout -- "$upstream" "$source_dir"
sed -n 's|^+++ b/|/|p' "${integration}/patches/"*.patch |
  git -C "$source_dir" sparse-checkout set --no-cone --stdin
git -C "$source_dir" checkout --quiet --detach "$commit"

"$prepare" --source "$source_dir" --check
assert_unmodified
printf 'PASS check-only leaves source and payload destinations untouched\n'

cp -R "$integration" "${results}/wrong-revision"
printf '%s\n' '0000000000000000000000000000000000000000' >"${results}/wrong-revision/upstream.commit"
expect_failure wrong-revision "$prepare" --source "$source_dir" --integration "${results}/wrong-revision"
assert_unmodified
printf 'PASS unsupported revision fails before mutation\n'

collision="${source_dir}/lib/auth/zitadel_adapter.go"
mkdir -p "$(dirname -- "$collision")"
printf '%s\n' 'existing developer file' >"$collision"
expect_failure destination-collision "$prepare" --source "$source_dir"
assert_unmodified
[[ "$(cat "$collision")" == 'existing developer file' ]] || fail 'existing developer file overwritten'
mv "$collision" "${results}/preserved-developer-file"
printf 'PASS existing overlay destination is preserved\n'

dirty="${source_dir}/lib/service/service.go"
printf '\n// Existing local edit.\n' >>"$dirty"
git -C "$source_dir" diff --binary >"${results}/before-conflict.patch"
expect_failure patch-conflict "$prepare" --source "$source_dir"
git -C "$source_dir" diff --binary | cmp - "${results}/before-conflict.patch"
[[ ! -e "${source_dir}/lib/zitadeloidc" ]] || fail 'failed patch check partially installed adapter'
cp "$dirty" "${results}/preserved-local-edit.go"
git -C "$source_dir" checkout -- lib/service/service.go
printf 'PASS modified patch target fails before any change\n'

"$prepare" --source "$source_dir"
git -C "$source_dir" diff --check
for file in "${repo_dir}/oidc-adapter/"*.go "${repo_dir}/oidc-adapter/config.example.json"; do
  cmp "$file" "${source_dir}/lib/zitadeloidc/$(basename -- "$file")"
done
while IFS= read -r -d '' file; do
  cmp "$file" "${source_dir}/${file#"${integration}/overlay/"}"
done < <(find "${integration}/overlay" -type f -print0)
git -C "$source_dir" diff --name-only | sort >"${results}/actual-paths"
sed -n 's|^+++ b/||p' "${integration}/patches/"*.patch | sort -u >"${results}/expected-paths"
cmp "${results}/actual-paths" "${results}/expected-paths"
printf 'PASS complete series and new files installed as expected\n'

git -C "$source_dir" diff --binary >"${results}/applied.patch"
expect_failure repeated-preparation "$prepare" --source "$source_dir"
git -C "$source_dir" diff --binary | cmp - "${results}/applied.patch"
printf 'PASS repeated preparation fails without changing prepared source\n'
printf 'Source-preparation fixtures and logs retained in %s\n' "$results"
