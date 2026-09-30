#!/usr/bin/env bash
set -euo pipefail

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/rhizome-demo-test.XXXXXX")"
TEST_ROOT="$(cd "$TEST_ROOT" && pwd -P)"
cleanup() {
  find "$TEST_ROOT" -type l -exec unlink {} \;
  find "$TEST_ROOT" -type f -exec rm -f -- {} +
  find "$TEST_ROOT" -depth -type d -exec rmdir -- {} \;
}
trap cleanup EXIT

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
HOME="$TEST_ROOT/home"
mkdir "$HOME" "$TEST_ROOT/unmarked" "$TEST_ROOT/invalid" "$TEST_ROOT/owned"
printf '%s\n' sentinel >"$HOME/sentinel"
printf '%s\n' sentinel >"$TEST_ROOT/unmarked/sentinel"
printf '%s\n' invalid >"$TEST_ROOT/invalid/.rhizome-demo-state"
printf '%s\n' rhizome-demo-state-v1 >"$TEST_ROOT/owned/.rhizome-demo-state"
printf '%s\n' rhizome-demo-state-v1 >"$HOME/.rhizome-demo-state"
printf '%s\n' rhizome-demo-state-v1 >"$TEST_ROOT/.rhizome-demo-state"
: >"$TEST_ROOT/deletions"
: >"$TEST_ROOT/bin-calls"

rm() {
  if [ "$#" -ne 3 ] || [ "$1" != -rf ] || [ "$2" != -- ] ||
     { [ "$3" != "$TEST_ROOT/owned" ] && [ "$3" != "$TEST_ROOT/fresh state" ]; }; then
    printf 'unexpected deletion: %s\n' "$*" >&2
    return 1
  fi
  printf '%s\n' "$3" >>"$TEST_ROOT/deletions"
}

demo_bin() {
  printf '%s\n' "$1" >>"$TEST_ROOT/bin-calls"
  case "$1" in
    init) [ -d "$PWD" ] ;;
    serve) printf '%s\n' 'listening on 127.0.0.1:12345' >&2 ;;
    *) return 1 ;;
  esac
}
BIN=demo_bin

reject_state() {
  local label="$1" candidate="$2"
  if (STATE="$candidate"; rz_start) >"$TEST_ROOT/result" 2>&1; then
    printf 'FAIL: accepted %s\n' "$label" >&2
    exit 1
  fi
  grep -q 'unsafe demo state' "$TEST_ROOT/result"
  [ ! -s "$TEST_ROOT/deletions" ]
  [ ! -s "$TEST_ROOT/bin-calls" ]
  [ "$(cat "$HOME/sentinel")" = sentinel ]
  [ "$(cat "$TEST_ROOT/unmarked/sentinel")" = sentinel ]
  printf 'PASS: rejected %s\n' "$label"
}

reject_state 'repository root' "$REPO_ROOT"
reject_state 'repository root via dot segments' "$REPO_ROOT/demo/.."
reject_state 'repository ancestor' "$(dirname "$REPO_ROOT")"
reject_state 'filesystem root' /
reject_state 'marked home' "$HOME"
reject_state 'marked home via trailing slash' "$HOME/"
reject_state 'marked home ancestor' "$TEST_ROOT"
reject_state 'unmarked directory' "$TEST_ROOT/unmarked"
reject_state 'invalid marker' "$TEST_ROOT/invalid"
reject_state 'regular file' "$HOME/sentinel"
(cd "$TEST_ROOT/unmarked"; reject_state 'working directory' .)

ln -s "$TEST_ROOT/owned" "$TEST_ROOT/alias"
reject_state 'symlink to owned directory' "$TEST_ROOT/alias"
reject_state 'symlink to owned directory with trailing slashes' "$TEST_ROOT/alias///"
ln -s "$TEST_ROOT/unmarked" "$TEST_ROOT/unmarked-alias"
reject_state 'symlink alias with trailing slash' "$TEST_ROOT/unmarked-alias/"
ln -s "$TEST_ROOT/missing" "$TEST_ROOT/dangling"
reject_state 'dangling symlink' "$TEST_ROOT/dangling"
mkdir "$TEST_ROOT/marker-link"
ln -s "$TEST_ROOT/owned/.rhizome-demo-state" "$TEST_ROOT/marker-link/.rhizome-demo-state"
reject_state 'symlink marker' "$TEST_ROOT/marker-link"
mkdir -p "$TEST_ROOT/marker-directory/.rhizome-demo-state"
reject_state 'directory marker' "$TEST_ROOT/marker-directory"

STATE="$TEST_ROOT/fresh state"
rz_start
[ ! -s "$TEST_ROOT/deletions" ]
[ -d "$STATE/project" ] && [ -d "$STATE/data" ]
[ "$(cat "$STATE/.rhizome-demo-state")" = rhizome-demo-state-v1 ]
[ "$(cat "$STATE/port")" = 12345 ]
printf 'PASS: initialized fresh directory and started server stub\n'

rz_start
[ "$(cat "$TEST_ROOT/deletions")" = "$STATE" ]
[ "$(cat "$STATE/port")" = 12345 ]
printf 'PASS: reset newly owned directory through intercepted deletion\n'

STATE="$TEST_ROOT/owned"
rz_start
[ "$(tail -1 "$TEST_ROOT/deletions")" = "$STATE" ]
[ "$(wc -l <"$TEST_ROOT/deletions" | tr -d ' ')" = 2 ]
[ "$(grep -c '^init$' "$TEST_ROOT/bin-calls")" = 3 ]
[ "$(grep -c '^serve$' "$TEST_ROOT/bin-calls")" = 3 ]
printf 'PASS: reset pre-existing owned directory through intercepted deletion\n'
