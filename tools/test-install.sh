#!/bin/sh

set -eu

repository_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P)
installer="${repository_root}/site/install.sh"
test_root=$(mktemp -d "${TMPDIR:-/tmp}/corresync-install-test.XXXXXX")

cleanup() {
  cleanup_status=$?
  trap - 0
  rm -rf -- "$test_root"
  exit "$cleanup_status"
}
trap cleanup 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf 'installer test failed: %s\n' "$*" >&2
  exit 1
}

assert_file_equals() {
  expected=$1
  actual=$2
  cmp -s "$expected" "$actual" || fail "$actual does not match $expected"
}

assert_contains() {
  pattern=$1
  path=$2
  grep -Fq "$pattern" "$path" || fail "$path does not contain: $pattern"
}

fixture_dir="${test_root}/fixtures"
fake_bin="${test_root}/fake-bin"
mkdir -p "$fixture_dir/archive" "$fake_bin"

cat >"$fixture_dir/archive/corr" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "version" ] && [ "${2:-}" = "--json" ]; then
  cat <<'JSON'
{
  "version": "9.8.7",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "buildDate": "2026-07-29T00:00:00Z",
  "goVersion": "go1.26.5",
  "os": "linux",
  "arch": "amd64"
}
JSON
  exit 0
fi
exit 2
EOF
chmod 0755 "$fixture_dir/archive/corr"
cp "$fixture_dir/archive/corr" "$fixture_dir/archive/corresync"

archive_name="corresync_9.8.7_linux_amd64.tar.gz"
tar -czf "$fixture_dir/$archive_name" -C "$fixture_dir/archive" corr corresync
archive_checksum=$(sha256sum "$fixture_dir/$archive_name" | awk '{ print $1 }')
printf '%s  %s\n' "$archive_checksum" "$archive_name" >"$fixture_dir/checksums.txt"
printf '{}\n' >"$fixture_dir/checksums.txt.sigstore.json"

cat >"$fake_bin/curl" <<'EOF'
#!/bin/sh
set -eu
output=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output | -o)
      output=$2
      shift 2
      ;;
    --proto | --retry | --retry-delay | --connect-timeout | --max-time | --write-out | -w)
      shift 2
      ;;
    --fail | --silent | --show-error | --location)
      shift
      ;;
    *)
      url=$1
      shift
      ;;
  esac
done
[ -n "$output" ] || exit 2
case "${url##*/}" in
  checksums.txt | checksums.txt.sigstore.json | corresync_9.8.7_linux_amd64.tar.gz)
    cp "${CORRESYNC_TEST_FIXTURES}/${url##*/}" "$output"
    ;;
  *)
    printf 'unexpected test URL: %s\n' "$url" >&2
    exit 2
    ;;
esac
EOF
chmod 0755 "$fake_bin/curl"

run_installer() {
  case_root=$1
  shift
  mkdir -p "$case_root/home"
  env \
    HOME="$case_root/home" \
    SHELL=/bin/bash \
    PATH="$fake_bin:/usr/bin:/bin" \
    CORRESYNC_TEST_FIXTURES="$fixture_dir" \
    CORRESYNC_VERSION=v9.8.7 \
    "$@" \
    /bin/sh "$installer" >"$case_root/stdout" 2>"$case_root/stderr"
}

fresh_case="${test_root}/fresh"
mkdir -p "$fresh_case"
run_installer "$fresh_case"
assert_file_equals "$fixture_dir/archive/corr" "$fresh_case/home/.local/bin/corr"
assert_file_equals "$fixture_dir/archive/corresync" "$fresh_case/home/.local/bin/corresync"
assert_contains "Verified the release archive SHA-256 checksum." "$fresh_case/stdout"
assert_contains "Sigstore provenance was not checked" "$fresh_case/stderr"
assert_contains '# >>> corresync >>>' "$fresh_case/home/.bashrc"
assert_contains "export PATH=\"\$HOME/.local/bin:\$PATH\"" "$fresh_case/home/.bashrc"
[ "$(grep -Fc '# >>> corresync >>>' "$fresh_case/home/.bashrc")" -eq 1 ] ||
  fail "PATH marker was not idempotent after first install"

run_installer "$fresh_case"
[ "$(grep -Fc '# >>> corresync >>>' "$fresh_case/home/.bashrc")" -eq 1 ] ||
  fail "PATH marker was duplicated after reinstall"

migration_case="${test_root}/migration"
mkdir -p "$migration_case/home/.local/bin"
printf 'directly updated v0.7 compatibility binary\n' >"$migration_case/home/.local/bin/corresync"
chmod 0755 "$migration_case/home/.local/bin/corresync"
run_installer "$migration_case"
assert_file_equals "$fixture_dir/archive/corr" "$migration_case/home/.local/bin/corr"
assert_file_equals "$fixture_dir/archive/corresync" "$migration_case/home/.local/bin/corresync"

custom_case="${test_root}/custom"
mkdir -p "$custom_case"
custom_install_dir="$custom_case/home/custom bin"
run_installer \
  "$custom_case" \
  CORRESYNC_INSTALL_DIR="$custom_install_dir" \
  CORRESYNC_NO_PATH_UPDATE=1
assert_file_equals "$fixture_dir/archive/corr" "$custom_install_dir/corr"
assert_file_equals "$fixture_dir/archive/corresync" "$custom_install_dir/corresync"
[ ! -e "$custom_case/home/.bashrc" ] ||
  fail "custom no-PATH-update install unexpectedly created .bashrc"

failure_case="${test_root}/failure"
mkdir -p "$failure_case/home/.local/bin"
printf 'working corr\n' >"$failure_case/home/.local/bin/corr"
printf 'working compatibility\n' >"$failure_case/home/.local/bin/corresync"
cp "$failure_case/home/.local/bin/corr" "$failure_case/corr.expected"
cp "$failure_case/home/.local/bin/corresync" "$failure_case/corresync.expected"
printf 'corrupt archive bytes\n' >"$fixture_dir/$archive_name"
if run_installer "$failure_case"; then
  fail "checksum mismatch unexpectedly succeeded"
fi
assert_file_equals "$failure_case/corr.expected" "$failure_case/home/.local/bin/corr"
assert_file_equals "$failure_case/corresync.expected" "$failure_case/home/.local/bin/corresync"
assert_contains "release archive checksum does not match" "$failure_case/stderr"
if find "$failure_case/home/.local/bin" -maxdepth 1 -name '.corresync-install.*' -print -quit |
  grep -q .; then
  fail "failed install left a transaction directory"
fi

printf 'standalone installer tests passed\n'
