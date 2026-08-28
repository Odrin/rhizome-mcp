#!/usr/bin/env bash
# Shared helpers for the scripted demos. Each driver is invoked one
# subcommand at a time by a VHS tape, so state lives in $RZ_DEMO_STATE.
# The MCP calls go over the local HTTP transport using the stateless
# 2026-07-28 flow documented in docs/08-local-http-transport.md.
set -euo pipefail

STATE="${RZ_DEMO_STATE:-${TMPDIR:-/tmp}/rhizome-demo-state}"
LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$LIB_DIR/.." && pwd)"
BIN="${RZ_DEMO_BIN:-$REPO_ROOT/rhizome-mcp}"

say()  { printf '\033[1;36m# %s\033[0m\n' "$*"; }
run()  { printf '\033[2m→\033[0m \033[1m%s\033[0m \033[2m%s\033[0m\n' "$1" "${2:-}"; }
ok()   { printf '\033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '\033[31m✗\033[0m %s\n' "$*"; }

rz_start() {
  rm -rf "$STATE"
  mkdir -p "$STATE/project" "$STATE/data"
  (cd "$STATE/project" && "$BIN" init --data-root "$STATE/data" >/dev/null 2>&1)
  "$BIN" serve --http-address 127.0.0.1:0 \
    --project-root "$STATE/project" --data-root "$STATE/data" \
    >/dev/null 2>"$STATE/serve.log" &
  echo $! >"$STATE/serve.pid"
  local port=""
  for _ in $(seq 1 100); do
    port="$(grep -oE '127\.0\.0\.1:[0-9]+' "$STATE/serve.log" | head -1 | cut -d: -f2 || true)"
    [ -n "$port" ] && break
    sleep 0.1
  done
  [ -n "$port" ] || { bad "server did not report a bound port"; cat "$STATE/serve.log"; exit 1; }
  echo "$port" >"$STATE/port"
}

rz_stop() {
  if [ -f "$STATE/serve.pid" ]; then
    kill "$(cat "$STATE/serve.pid")" >/dev/null 2>&1 || true
  fi
}

# rz_call TOOL ARGS_JSON -> raw jsonrpc response body (SSE framing stripped)
rz_call() {
  local tool="$1" args="$2" port body
  port="$(cat "$STATE/port")"
  body="$(jq -nc --arg t "$tool" --argjson a "$args" \
    '{jsonrpc:"2.0",id:"1",method:"tools/call",params:{name:$t,arguments:$a,_meta:{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}')"
  curl -s -X POST "http://127.0.0.1:$port/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H 'Mcp-Protocol-Version: 2026-07-28' \
    -H 'Mcp-Method: tools/call' \
    -H "Mcp-Name: $tool" \
    -d "$body" | sed -n 's/^data: //p; /^data:/!p' | sed '/^event:/d; /^$/d'
}

# rz_result TOOL ARGS_JSON -> structuredContent JSON; exits nonzero on tool error
rz_result() {
  local out
  out="$(rz_call "$1" "$2")"
  if [ "$(jq -r '.result.isError // false' <<<"$out")" = "true" ]; then
    jq -r '.result.content[0].text' <<<"$out" >&2
    return 1
  fi
  jq -c '.result.structuredContent' <<<"$out"
}

# rz_error TOOL ARGS_JSON -> error text on stdout (stable code plus the most
# specific detail message available); fails if the call succeeded
rz_error() {
  local out
  out="$(rz_call "$1" "$2")"
  if [ "$(jq -r '.result.isError // false' <<<"$out")" = "true" ]; then
    jq -r 'if .result.structuredContent.details[0].message? then
             .result.structuredContent.code + ": " + .result.structuredContent.details[0].message
           else .result.content[0].text end' <<<"$out"
    return 0
  fi
  echo "expected an error but the call succeeded" >&2
  return 1
}

rz_board() { (cd "$STATE/project" && "$BIN" board --data-root "$STATE/data" "$@"); }

state_get() { cat "$STATE/$1"; }
state_set() { printf '%s' "$2" >"$STATE/$1"; }
