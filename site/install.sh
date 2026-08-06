#!/bin/sh

# Install the latest stable Corresync release on macOS or Linux without sudo.
# Review this script before running it:
#   https://corresync.org/install.sh
#
# Optional environment variables:
#   CORRESYNC_VERSION=v0.8.0
#   CORRESYNC_INSTALL_DIR="$HOME/.local/bin"
#   CORRESYNC_NO_PATH_UPDATE=1

set -eu
umask 077

repository_url="https://github.com/nkiyohara/corresync"
latest_url="${repository_url}/releases/latest"
workflow_identity_prefix="${repository_url}/.github/workflows/release.yml@refs/tags/"
oidc_issuer="https://token.actions.githubusercontent.com"

work_dir=""
transaction_dir=""
install_dir=""
corr_target=""
compat_target=""
corr_had_old=0
compat_had_old=0
corr_installed=0
compat_installed=0
committed=0

say() {
  printf '%s\n' "$*"
}

warn() {
  printf 'warning: %s\n' "$*" >&2
}

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  cleanup_status=$?
  trap - 0

  if [ "$committed" -eq 0 ] && [ -n "$transaction_dir" ]; then
    if [ "$compat_installed" -eq 1 ]; then
      rm -f -- "$compat_target"
    fi
    if [ "$compat_had_old" -eq 1 ] && [ -f "$transaction_dir/corresync.old" ]; then
      mv -f -- "$transaction_dir/corresync.old" "$compat_target"
    fi
    if [ "$corr_installed" -eq 1 ]; then
      rm -f -- "$corr_target"
    fi
    if [ "$corr_had_old" -eq 1 ] && [ -f "$transaction_dir/corr.old" ]; then
      mv -f -- "$transaction_dir/corr.old" "$corr_target"
    fi
  fi

  if [ -n "$transaction_dir" ] && [ -d "$transaction_dir" ]; then
    rm -rf -- "$transaction_dir"
  fi
  if [ -n "$work_dir" ] && [ -d "$work_dir" ]; then
    rm -rf -- "$work_dir"
  fi

  exit "$cleanup_status"
}

trap cleanup 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

need_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

download() {
  download_url=$1
  download_target=$2
  curl \
    --proto '=https' \
    --proto-redir '=https' \
    --tlsv1.2 \
    --fail \
    --silent \
    --show-error \
    --location \
    --retry 3 \
    --retry-delay 1 \
    --connect-timeout 10 \
    --max-time 300 \
    --output "$download_target" \
    "$download_url"
}

file_size() {
  wc -c <"$1" | tr -d '[:space:]'
}

file_owner() {
  case "$operating_system" in
    darwin) stat -f '%u' "$1" ;;
    linux) stat -c '%u' "$1" ;;
  esac
}

file_mode() {
  case "$operating_system" in
    darwin) stat -f '%Lp' "$1" ;;
    linux) stat -c '%a' "$1" ;;
  esac
}

sha256_file() {
  case "$operating_system" in
    darwin) shasum -a 256 "$1" | awk '{ print $1 }' ;;
    linux) sha256sum "$1" | awk '{ print $1 }' ;;
  esac
}

run_with_timeout() {
  timeout_seconds=$1
  shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "$timeout_seconds" "$@"
    return
  fi

  "$@" &
  command_pid=$!
  (
    sleep "$timeout_seconds"
    if kill -0 "$command_pid" 2>/dev/null; then
      kill -TERM "$command_pid" 2>/dev/null || :
      sleep 1
      kill -KILL "$command_pid" 2>/dev/null || :
    fi
  ) &
  watchdog_pid=$!

  if wait "$command_pid"; then
    command_status=0
  else
    command_status=$?
  fi
  kill "$watchdog_pid" 2>/dev/null || :
  wait "$watchdog_pid" 2>/dev/null || :
  return "$command_status"
}

require_bounded_file() {
  bounded_path=$1
  bounded_limit=$2
  bounded_label=$3
  bounded_size=$(file_size "$bounded_path")
  case "$bounded_size" in
    '' | *[!0-9]*)
      fail "could not determine ${bounded_label} size"
      ;;
  esac
  [ "$bounded_size" -le "$bounded_limit" ] ||
    fail "${bounded_label} exceeds the ${bounded_limit}-byte limit"
}

json_string_field() {
  json_key=$1
  awk -F '"' -v key="$json_key" '$2 == key { print $4; exit }'
}

need_command awk
need_command chmod
need_command cmp
need_command curl
need_command grep
need_command id
need_command install
need_command mkdir
need_command mktemp
need_command mv
need_command sleep
need_command stat
need_command tar
need_command tr
need_command uname
need_command wc

case "$(uname -s)" in
  Darwin)
    operating_system="darwin"
    platform_label="macOS"
    need_command shasum
    ;;
  Linux)
    operating_system="linux"
    platform_label="Linux"
    need_command sha256sum
    ;;
  *)
    fail "the shell installer supports macOS and Linux; use install.ps1 on Windows"
    ;;
esac

case "$(uname -m)" in
  x86_64 | amd64)
    architecture="amd64"
    ;;
  aarch64 | arm64)
    architecture="arm64"
    ;;
  *)
    fail "unsupported ${platform_label} architecture: $(uname -m)"
    ;;
esac

requested_version=${CORRESYNC_VERSION:-}
if [ -n "$requested_version" ]; then
  case "$requested_version" in
    v*)
      tag=$requested_version
      ;;
    *)
      tag="v${requested_version}"
      ;;
  esac
else
  effective_url=$(
    curl \
      --proto '=https' \
      --proto-redir '=https' \
      --tlsv1.2 \
      --fail \
      --silent \
      --show-error \
      --location \
      --retry 3 \
      --retry-delay 1 \
      --connect-timeout 10 \
      --max-time 60 \
      --output /dev/null \
      --write-out '%{url_effective}' \
      "$latest_url"
  )
  effective_url=${effective_url%/}
  case "$effective_url" in
    "${repository_url}/releases/tag/"*)
      tag=${effective_url##*/}
      ;;
    *)
      fail "latest release redirected outside the canonical repository"
      ;;
  esac
fi

printf '%s\n' "$tag" |
  grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' ||
  fail "version must be a stable tag such as v0.8.0"

version=${tag#v}
archive_name="corresync_${version}_${operating_system}_${architecture}.tar.gz"
release_url="${repository_url}/releases/download/${tag}"

home_dir=${HOME:-}
[ -n "$home_dir" ] || fail "HOME is not set"
case "$home_dir" in
  /*) ;;
  *) fail "HOME must be an absolute path" ;;
esac

default_install_dir="${home_dir}/.local/bin"
install_dir=${CORRESYNC_INSTALL_DIR:-$default_install_dir}
case "$install_dir" in
  /*) ;;
  *) fail "CORRESYNC_INSTALL_DIR must be an absolute path" ;;
esac
case "$install_dir" in
  *'
'*) fail "CORRESYNC_INSTALL_DIR must not contain a newline" ;;
esac

work_parent=${TMPDIR:-/tmp}
case "$work_parent" in
  /*) ;;
  *) fail "TMPDIR must be an absolute path" ;;
esac
work_dir=$(mktemp -d "${work_parent%/}/corresync-install.XXXXXX")
chmod 0700 "$work_dir"

manifest_path="${work_dir}/checksums.txt"
bundle_path="${work_dir}/checksums.txt.sigstore.json"
archive_path="${work_dir}/${archive_name}"

say "Downloading Corresync ${tag} for ${operating_system}/${architecture}..."
download "${release_url}/checksums.txt" "$manifest_path"
download "${release_url}/checksums.txt.sigstore.json" "$bundle_path"
download "${release_url}/${archive_name}" "$archive_path"

require_bounded_file "$manifest_path" 65536 "checksum manifest"
require_bounded_file "$bundle_path" 1048576 "Sigstore bundle"
require_bounded_file "$archive_path" 67108864 "release archive"

expected_checksum=$(
  awk -v name="$archive_name" '
    $2 == name { count++; checksum = $1 }
    END {
      if (count == 1) {
        print checksum
      } else {
        exit 1
      }
    }
  ' "$manifest_path"
) || fail "checksum manifest must contain exactly one ${archive_name} entry"

case "$expected_checksum" in
  '' | *[!0-9a-fA-F]*) fail "release archive checksum is not hexadecimal" ;;
esac
[ "${#expected_checksum}" -eq 64 ] ||
  fail "release archive checksum must contain 64 hexadecimal characters"

actual_checksum=$(sha256_file "$archive_path")
[ "$actual_checksum" = "$expected_checksum" ] ||
  fail "release archive checksum does not match checksums.txt"
say "Verified the release archive SHA-256 checksum."

if command -v cosign >/dev/null 2>&1; then
  cosign verify-blob \
    --bundle "$bundle_path" \
    --certificate-identity "${workflow_identity_prefix}${tag}" \
    --certificate-oidc-issuer "$oidc_issuer" \
    "$manifest_path" >/dev/null
  say "Verified the exact GitHub Actions Sigstore identity."
else
  warn "cosign was not found; SHA-256 is verified, but Sigstore provenance was not checked"
fi

extract_dir="${work_dir}/extract"
mkdir "$extract_dir"

listing_path="${work_dir}/archive-entries.txt"
if ! (
  ulimit -f 512
  run_with_timeout 30 tar -tzf "$archive_path" >"$listing_path"
); then
  fail "release archive inventory is invalid, too large, or took too long to parse"
fi
require_bounded_file "$listing_path" 262144 "release archive inventory"
archive_entries=$(wc -l <"$listing_path" | tr -d '[:space:]')
case "$archive_entries" in
  '' | *[!0-9]*) fail "could not count release archive entries" ;;
esac
[ "$archive_entries" -le 4096 ] || fail "release archive contains more than 4096 entries"

corr_entries=$(awk '$0 == "corr" { count++ } END { print count + 0 }' "$listing_path")
compat_entries=$(
  awk '$0 == "corresync" { count++ } END { print count + 0 }' "$listing_path"
)
[ "$corr_entries" -eq 1 ] || fail "release archive must contain exactly one corr entry"
[ "$compat_entries" -eq 1 ] ||
  fail "release archive must contain exactly one corresync compatibility entry"

for candidate_name in corr corresync; do
  candidate_path="${extract_dir}/${candidate_name}"
  if ! (
    ulimit -f 131072
    run_with_timeout 30 tar -xOzf "$archive_path" "$candidate_name" >"$candidate_path"
  ); then
    fail "release archive ${candidate_name} entry is invalid, too large, or took too long to extract"
  fi
  [ -f "$candidate_path" ] || fail "release archive ${candidate_name} entry is not a regular file"
  require_bounded_file "$candidate_path" 67108864 "${candidate_name} executable"
  chmod 0755 "$candidate_path"
done

cmp -s "$extract_dir/corr" "$extract_dir/corresync" ||
  fail "corr and corresync compatibility executables are not identical"

candidate_json_path="${work_dir}/candidate-version.json"
if ! (
  ulimit -f 128
  run_with_timeout 5 "$extract_dir/corr" version --json >"$candidate_json_path"
); then
  fail "candidate version check failed, produced too much output, or took too long"
fi
require_bounded_file "$candidate_json_path" 65536 "candidate version response"
candidate_json=$(cat "$candidate_json_path")
candidate_version=$(printf '%s\n' "$candidate_json" | json_string_field version)
candidate_os=$(printf '%s\n' "$candidate_json" | json_string_field os)
candidate_arch=$(printf '%s\n' "$candidate_json" | json_string_field arch)
[ "$candidate_version" = "$version" ] ||
  fail "candidate version ${candidate_version:-unknown} does not match ${version}"
[ "$candidate_os" = "$operating_system" ] ||
  fail "candidate operating system ${candidate_os:-unknown} is not ${operating_system}"
[ "$candidate_arch" = "$architecture" ] ||
  fail "candidate architecture ${candidate_arch:-unknown} does not match ${architecture}"
say "Validated candidate version, operating system, and architecture."

if [ -L "$install_dir" ]; then
  fail "install directory must not be a symbolic link: $install_dir"
fi
mkdir -p "$install_dir"
[ -d "$install_dir" ] && [ ! -L "$install_dir" ] ||
  fail "install target is not a regular directory: $install_dir"

install_owner=$(file_owner "$install_dir")
[ "$install_owner" = "$(id -u)" ] ||
  fail "install directory must be owned by the current user: $install_dir"
install_mode=$(file_mode "$install_dir")
case "$install_mode" in
  '' | *[!0-7]*) fail "could not determine install directory permissions" ;;
esac
install_mode_value=$((0$install_mode))
[ $((install_mode_value & 0022)) -eq 0 ] ||
  fail "install directory must not be group- or world-writable: $install_dir"

install_dir=$(CDPATH='' cd -- "$install_dir" && pwd -P)
corr_target="${install_dir}/corr"
compat_target="${install_dir}/corresync"

for target_path in "$corr_target" "$compat_target"; do
  if [ -L "$target_path" ]; then
    fail "refusing to replace a symbolic link: $target_path"
  fi
  if [ -e "$target_path" ]; then
    [ -f "$target_path" ] || fail "refusing to replace a non-regular file: $target_path"
    [ "$(file_owner "$target_path")" = "$(id -u)" ] ||
      fail "refusing to replace a file not owned by the current user: $target_path"
  fi
done

transaction_dir=$(mktemp -d "${install_dir}/.corresync-install.XXXXXX")
chmod 0700 "$transaction_dir"
install -m 0755 "$extract_dir/corr" "$transaction_dir/corr.new"
install -m 0755 "$extract_dir/corresync" "$transaction_dir/corresync.new"

if [ -e "$corr_target" ]; then
  mv -- "$corr_target" "$transaction_dir/corr.old"
  corr_had_old=1
fi
mv -- "$transaction_dir/corr.new" "$corr_target"
corr_installed=1

if [ -e "$compat_target" ]; then
  mv -- "$compat_target" "$transaction_dir/corresync.old"
  compat_had_old=1
fi
mv -- "$transaction_dir/corresync.new" "$compat_target"
compat_installed=1

committed=1
rm -f -- "$transaction_dir/corr.old" "$transaction_dir/corresync.old"
rmdir "$transaction_dir"
transaction_dir=""

path_updated=0
case ":${PATH:-}:" in
  *":${install_dir}:"*) ;;
  *)
    no_path_update=${CORRESYNC_NO_PATH_UPDATE:-}
    case "$no_path_update" in
      1 | true | TRUE | yes | YES) ;;
      *)
        if [ "$install_dir" = "$default_install_dir" ]; then
          case "${SHELL:-}" in
            */bash) profile_path="${home_dir}/.bashrc" ;;
            */zsh) profile_path="${home_dir}/.zshrc" ;;
            *) profile_path="${home_dir}/.profile" ;;
          esac
          if [ -L "$profile_path" ]; then
            warn "did not edit symlinked shell profile: $profile_path"
          elif [ -e "$profile_path" ] && [ ! -f "$profile_path" ]; then
            warn "did not edit non-regular shell profile: $profile_path"
          elif [ -e "$profile_path" ] && [ "$(file_owner "$profile_path")" != "$(id -u)" ]; then
            warn "did not edit shell profile not owned by the current user: $profile_path"
          elif [ -e "$profile_path" ] &&
            [ $((0$(file_mode "$profile_path") & 0022)) -ne 0 ]; then
            warn "did not edit group- or world-writable shell profile: $profile_path"
          else
            if [ ! -e "$profile_path" ]; then
              : >"$profile_path"
              chmod 0600 "$profile_path"
            fi
            if ! grep -Fq '# >>> corresync >>>' "$profile_path"; then
              # The startup file must retain these HOME and PATH references.
              # shellcheck disable=SC2016
              if printf '\n# >>> corresync >>>\nexport PATH="$HOME/.local/bin:$PATH"\n# <<< corresync <<<\n' \
                >>"$profile_path"; then
                path_updated=1
              else
                warn "could not update PATH in $profile_path"
              fi
            fi
          fi
        fi
        ;;
    esac
    ;;
esac

say ""
say "Installed Corresync ${version}:"
say "  ${corr_target}"
say "  ${compat_target} (v0.8-v0.9 compatibility)"
if [ "$path_updated" -eq 1 ]; then
  say "Added ${install_dir} to PATH for new shells. Open a new terminal before using corr."
else
  case ":${PATH:-}:" in
    *":${install_dir}:"*) ;;
    *)
      say "Add ${install_dir} to PATH, then open a new terminal."
      ;;
  esac
fi
say ""
say "Next:"
say "  corr setup you@example.com --alias personal"
say "  corr auth login --account personal"
say "  corr mcp setup codex"
say ""
say "No account was configured, no login was started, and no MCP client was changed."
