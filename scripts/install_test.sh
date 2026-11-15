#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
INSTALL_SCRIPT="${SCRIPT_DIR}/install.sh"
INSTALL_TEST_SHELL="${INSTALL_TEST_SHELL:-/bin/sh}"
ORIG_PATH="$PATH"

TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT INT TERM

PASSED=0
FAILED=0

log_pass() {
  printf 'PASS: %s\n' "$1"
  PASSED=$((PASSED + 1))
}

log_fail() {
  printf 'FAIL: %s\n' "$1" >&2
  FAILED=$((FAILED + 1))
}

assert_file_contains() {
  file="$1"
  pattern="$2"
  desc="$3"
  if ! grep -Fq -- "$pattern" "$file"; then
    printf 'Assertion failed (%s): expected %s to contain %s\n' "$desc" "$file" "$pattern" >&2
    cat "$file" >&2 || true
    return 1
  fi
}

assert_file_not_contains() {
  file="$1"
  pattern="$2"
  desc="$3"
  if grep -Fq -- "$pattern" "$file"; then
    printf 'Assertion failed (%s): expected %s NOT to contain %s\n' "$desc" "$file" "$pattern" >&2
    cat "$file" >&2 || true
    return 1
  fi
}

detect_host_hasher() {
  HOST_HASHER_KIND="fallback"
  HOST_HASHER_BIN=""
  if p="$(PATH="$ORIG_PATH" command -v sha256sum 2>/dev/null || true)" && [ -n "$p" ]; then
    HOST_HASHER_KIND="sha256sum"
    HOST_HASHER_BIN="$p"
  elif p="$(PATH="$ORIG_PATH" command -v shasum 2>/dev/null || true)" && [ -n "$p" ]; then
    HOST_HASHER_KIND="shasum"
    HOST_HASHER_BIN="$p"
  elif p="$(PATH="$ORIG_PATH" command -v openssl 2>/dev/null || true)" && [ -n "$p" ]; then
    HOST_HASHER_KIND="openssl"
    HOST_HASHER_BIN="$p"
  fi
}

# Build a real local tarball containing a test rhizome-mcp executable.
FIXTURE_DIR="${TEST_ROOT}/fixture"
mkdir -p "${FIXTURE_DIR}/payload"
cat > "${FIXTURE_DIR}/payload/rhizome-mcp" <<'EOF'
#!/bin/sh
echo "rhizome-mcp-test-binary v1.2.3"
EOF
chmod 0755 "${FIXTURE_DIR}/payload/rhizome-mcp"

ARCHIVE_FILE="${FIXTURE_DIR}/rhizome-mcp-test.tar.gz"
tar -czf "$ARCHIVE_FILE" -C "${FIXTURE_DIR}/payload" rhizome-mcp

detect_host_hasher

MOCK_HASH_HELPER="${FIXTURE_DIR}/mock_hash.sh"
cat > "$MOCK_HASH_HELPER" <<'EOF'
#!/bin/sh
set -eu
target="$1"
case "$HOST_HASHER_KIND" in
  sha256sum)
    "$HOST_HASHER_BIN" "$target"
    ;;
  shasum)
    digest="$("$HOST_HASHER_BIN" -a 256 "$target" | awk '{print $1}')"
    printf '%s  %s\n' "$digest" "$target"
    ;;
  openssl)
    digest="$("$HOST_HASHER_BIN" dgst -sha256 "$target" | awk '{print $NF}')"
    printf '%s  %s\n' "$digest" "$target"
    ;;
  *)
    printf '%s  %s\n' "$MOCK_FALLBACK_HASH" "$target"
    ;;
esac
EOF
chmod 0755 "$MOCK_HASH_HELPER"

FALLBACK_HASH="e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
ARCHIVE_SHA256="$(
  HOST_HASHER_KIND="$HOST_HASHER_KIND" \
  HOST_HASHER_BIN="$HOST_HASHER_BIN" \
  MOCK_FALLBACK_HASH="$FALLBACK_HASH" \
  "$MOCK_HASH_HELPER" "$ARCHIVE_FILE" | awk '{print $1}'
)"
CHECKSUM_FILE="${FIXTURE_DIR}/rhizome-mcp-test.tar.gz.sha256"
printf '%s\n' "$ARCHIVE_SHA256" > "$CHECKSUM_FILE"

setup_mock_bin() {
  bin_dir="$1"
  tool_mode="$2"

  rm -rf "$bin_dir"
  mkdir -p "$bin_dir"

  for cmd in sh uname tr sed head mktemp rm cat awk mkdir install tar cp gzip gunzip; do
    resolved="$(PATH="$ORIG_PATH" command -v "$cmd" 2>/dev/null || true)"
    case "$resolved" in
      /*)
        ln -s "$resolved" "${bin_dir}/${cmd}"
        ;;
    esac
  done

  cat > "${bin_dir}/curl" <<'EOF'
#!/bin/sh
set -eu
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      shift
      if [ "$#" -eq 0 ]; then
        echo "mock curl: missing argument for -o" >&2
        exit 2
      fi
      out="$1"
      ;;
    -*)
      ;;
    *)
      url="$1"
      ;;
  esac
  shift
done

case "$url" in
  https://api.github.com/repos/*/releases/*)
    printf '{"tag_name":"%s"}\n' "${MOCK_TAG:-v1.2.3}"
    ;;
  *.tar.gz)
    cp "$MOCK_ARCHIVE_FILE" "$out"
    ;;
  *.tar.gz.sha256)
    if [ "${MOCK_CURL_SKIP_CHECKSUM:-0}" = "1" ]; then
      exit 0
    fi
    if [ "${MOCK_CURL_FAIL_CHECKSUM:-0}" = "1" ]; then
      echo "curl: (22) The requested URL returned error: 404" >&2
      exit 22
    fi
    cp "$MOCK_CHECKSUM_FILE" "$out"
    ;;
  *)
    echo "mock curl: unexpected URL: $url" >&2
    exit 1
    ;;
esac
EOF
  chmod 0755 "${bin_dir}/curl"

  if [ "$tool_mode" = "sha256sum" ] || [ "$tool_mode" = "both" ]; then
    cat > "${bin_dir}/sha256sum" <<'EOF'
#!/bin/sh
set -eu
if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
  echo "mock sha256sum: expected single existing file argument" >&2
  exit 1
fi
if [ "${MOCK_SHA256SUM_FAIL:-0}" = "1" ]; then
  echo "mock sha256sum: I/O error" >&2
  exit 2
fi
if [ -n "${MOCK_CALL_LOG:-}" ]; then
  printf 'sha256sum %s\n' "$1" >> "$MOCK_CALL_LOG"
fi
exec "$MOCK_HASH_HELPER" "$1"
EOF
    chmod 0755 "${bin_dir}/sha256sum"
  fi

  if [ "$tool_mode" = "shasum" ] || [ "$tool_mode" = "both" ]; then
    cat > "${bin_dir}/shasum" <<'EOF'
#!/bin/sh
set -eu
if [ "$#" -ne 3 ] || [ "${1:-}" != "-a" ] || [ "${2:-}" != "256" ] || [ ! -f "${3:-}" ]; then
  echo "mock shasum: expected -a 256 <file>" >&2
  exit 1
fi
if [ -n "${MOCK_CALL_LOG:-}" ]; then
  printf 'shasum %s %s %s\n' "$1" "$2" "$3" >> "$MOCK_CALL_LOG"
fi
exec "$MOCK_HASH_HELPER" "$3"
EOF
    chmod 0755 "${bin_dir}/shasum"
  fi
}

run_case() {
  case_name="$1"
  tool_mode="$2"
  checksum_override="${3:-$CHECKSUM_FILE}"
  skip_checksum="${4:-0}"
  fail_checksum="${5:-0}"
  fail_sha256sum="${6:-0}"

  case_dir="${TEST_ROOT}/case_${case_name}"
  mock_bin="${case_dir}/bin"
  install_dir="${case_dir}/install_bin"
  case_home="${case_dir}/home"
  mkdir -p "$case_dir" "$case_home"

  CASE_STDOUT="${case_dir}/stdout.log"
  CASE_STDERR="${case_dir}/stderr.log"
  CASE_CALL_LOG="${case_dir}/calls.log"
  CASE_INSTALL_DIR="$install_dir"
  : > "$CASE_CALL_LOG"

  setup_mock_bin "$mock_bin" "$tool_mode"

  set +e
  PATH="$mock_bin" \
    HOME="$case_home" \
    RHIZOME_INSTALL_DIR="$install_dir" \
    RHIZOME_VERSION="v1.2.3" \
    MOCK_TAG="v1.2.3" \
    MOCK_ARCHIVE_FILE="$ARCHIVE_FILE" \
    MOCK_CHECKSUM_FILE="$checksum_override" \
    MOCK_CURL_SKIP_CHECKSUM="$skip_checksum" \
    MOCK_CURL_FAIL_CHECKSUM="$fail_checksum" \
    MOCK_SHA256SUM_FAIL="$fail_sha256sum" \
    MOCK_CALL_LOG="$CASE_CALL_LOG" \
    MOCK_HASH_HELPER="$MOCK_HASH_HELPER" \
    HOST_HASHER_KIND="$HOST_HASHER_KIND" \
    HOST_HASHER_BIN="$HOST_HASHER_BIN" \
    MOCK_FALLBACK_HASH="$ARCHIVE_SHA256" \
    "$INSTALL_TEST_SHELL" "$INSTALL_SCRIPT" >"$CASE_STDOUT" 2>"$CASE_STDERR"
  CASE_RC=$?
  set -e
}

verify_installed_binary() {
  bin_path="${CASE_INSTALL_DIR}/rhizome-mcp"
  if [ ! -x "$bin_path" ]; then
    printf 'Expected executable binary at %s\n' "$bin_path" >&2
    return 1
  fi
  output="$("$bin_path")"
  if [ "$output" != "rhizome-mcp-test-binary v1.2.3" ]; then
    printf 'Unexpected installed binary output: %s\n' "$output" >&2
    return 1
  fi
}

verify_not_installed() {
  bin_path="${CASE_INSTALL_DIR}/rhizome-mcp"
  if [ -e "$bin_path" ]; then
    printf 'Expected binary NOT to be installed at %s\n' "$bin_path" >&2
    return 1
  fi
}

test_sha256sum_only() {
  run_case "sha256sum_only" "sha256sum"
  if [ "$CASE_RC" -ne 0 ]; then
    printf 'Expected exit 0, got %s\n' "$CASE_RC" >&2
    cat "$CASE_STDERR" >&2 || true
    return 1
  fi
  verify_installed_binary
  assert_file_contains "$CASE_CALL_LOG" "sha256sum " "sha256sum invoked"
  assert_file_not_contains "$CASE_CALL_LOG" "shasum " "shasum not invoked"
}

test_shasum_only() {
  run_case "shasum_only" "shasum"
  if [ "$CASE_RC" -ne 0 ]; then
    printf 'Expected exit 0, got %s\n' "$CASE_RC" >&2
    cat "$CASE_STDERR" >&2 || true
    return 1
  fi
  verify_installed_binary
  assert_file_contains "$CASE_CALL_LOG" "shasum -a 256 " "shasum -a 256 invoked"
  assert_file_not_contains "$CASE_CALL_LOG" "sha256sum " "sha256sum not invoked"
}

test_prefers_sha256sum_over_shasum() {
  run_case "both_tools" "both"
  if [ "$CASE_RC" -ne 0 ]; then
    printf 'Expected exit 0, got %s\n' "$CASE_RC" >&2
    cat "$CASE_STDERR" >&2 || true
    return 1
  fi
  verify_installed_binary
  assert_file_contains "$CASE_CALL_LOG" "sha256sum " "sha256sum preferred"
  assert_file_not_contains "$CASE_CALL_LOG" "shasum " "shasum skipped when sha256sum present"
}

test_neither_tool_present() {
  run_case "neither_tool" "none"
  if [ "$CASE_RC" -ne 1 ]; then
    printf 'Expected exit 1 when neither sha256sum nor shasum is present, got %s\n' "$CASE_RC" >&2
    return 1
  fi
  assert_file_contains "$CASE_STDERR" "sha256sum" "stderr mentions sha256sum"
  assert_file_contains "$CASE_STDERR" "shasum" "stderr mentions shasum"
  verify_not_installed
}

test_checksum_mismatch() {
  bad_checksum="${TEST_ROOT}/mismatch.sha256"
  printf '0000000000000000000000000000000000000000000000000000000000000000\n' > "$bad_checksum"
  run_case "checksum_mismatch" "sha256sum" "$bad_checksum"
  if [ "$CASE_RC" -ne 1 ]; then
    printf 'Expected exit 1 on checksum mismatch, got %s\n' "$CASE_RC" >&2
    return 1
  fi
  assert_file_contains "$CASE_STDERR" "Checksum verification failed" "stderr mentions checksum mismatch"
  verify_not_installed
}

test_checksum_normalization_crlf_spaces() {
  padded_checksum="${TEST_ROOT}/padded.sha256"
  printf '  %s  \r\n' "$ARCHIVE_SHA256" > "$padded_checksum"
  run_case "checksum_padded" "sha256sum" "$padded_checksum"
  if [ "$CASE_RC" -ne 0 ]; then
    printf 'Expected exit 0 for whitespace/CRLF-padded checksum, got %s\n' "$CASE_RC" >&2
    cat "$CASE_STDERR" >&2 || true
    return 1
  fi
  verify_installed_binary
}

test_empty_checksum_fails() {
  zero_checksum="${TEST_ROOT}/zero.sha256"
  : > "$zero_checksum"
  run_case "zero_checksum" "sha256sum" "$zero_checksum"
  if [ "$CASE_RC" -ne 1 ]; then
    printf 'Expected exit 1 on 0-byte checksum, got %s\n' "$CASE_RC" >&2
    return 1
  fi
  assert_file_contains "$CASE_STDERR" "Checksum verification failed" "stderr mentions checksum failure for 0-byte checksum"
  verify_not_installed

  ws_checksum="${TEST_ROOT}/ws.sha256"
  printf '   \r\n' > "$ws_checksum"
  run_case "ws_checksum" "sha256sum" "$ws_checksum"
  if [ "$CASE_RC" -ne 1 ]; then
    printf 'Expected exit 1 on whitespace-only checksum, got %s\n' "$CASE_RC" >&2
    return 1
  fi
  assert_file_contains "$CASE_STDERR" "Checksum verification failed" "stderr mentions checksum failure for whitespace-only checksum"
  verify_not_installed
}

test_missing_checksum_file_fails() {
  run_case "missing_checksum_file" "sha256sum" "$CHECKSUM_FILE" 1 0
  if [ "$CASE_RC" -ne 1 ]; then
    printf 'Expected exit 1 when checksum file is missing, got %s\n' "$CASE_RC" >&2
    return 1
  fi
  assert_file_contains "$CASE_STDERR" "Checksum verification failed" "stderr mentions missing checksum file"
  verify_not_installed

  run_case "curl_fail_checksum" "sha256sum" "$CHECKSUM_FILE" 0 1
  if [ "$CASE_RC" -eq 0 ]; then
    printf 'Expected non-zero exit when checksum download fails\n' >&2
    return 1
  fi
  verify_not_installed
}

test_sha256sum_failure_aborts() {
  run_case "sha256sum_fail" "sha256sum" "$CHECKSUM_FILE" 0 0 1
  if [ "$CASE_RC" -eq 0 ]; then
    printf 'Expected non-zero exit when sha256sum fails\n' >&2
    return 1
  fi
  verify_not_installed
}

run_test() {
  name="$1"
  fn="$2"
  set +e
  ( "$fn" )
  rc=$?
  set -e
  if [ "$rc" -eq 0 ]; then
    log_pass "$name"
  else
    log_fail "$name"
  fi
}

run_test "sha256sum-only environment succeeds" test_sha256sum_only
run_test "shasum-only environment succeeds" test_shasum_only
run_test "prefers sha256sum when both tools present" test_prefers_sha256sum_over_shasum
run_test "fails clearly when neither sha256sum nor shasum is present" test_neither_tool_present
run_test "fails on checksum mismatch without installing binary" test_checksum_mismatch
run_test "normalizes spaces and CRLF in checksum file" test_checksum_normalization_crlf_spaces
run_test "fails on empty checksum without installing binary" test_empty_checksum_fails
run_test "fails on missing checksum file without installing binary" test_missing_checksum_file_fails
run_test "fails when sha256sum exits non-zero without installing binary" test_sha256sum_failure_aborts

printf 'Summary: %d passed, %d failed\n' "$PASSED" "$FAILED"
[ "$FAILED" -eq 0 ]
