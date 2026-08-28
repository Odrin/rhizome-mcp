#!/usr/bin/env bash
# Scenario A: crash-safe leases. Two logical agent sessions work one issue;
# agent-a dies without cleanup, its lease expires, agent-b resumes from the
# checkpoint. Invoked one subcommand at a time by tapes/01-lease-expiry.tape;
# `all` runs the full flow (with a real 65s wait) as a smoke test.
#
# Arguments are built with jq -nc rather than inline escaped JSON: macOS
# bash 3.2 mis-parses nested quotes inside $(...) and brace-expands the
# payload.
set -euo pipefail
export RZ_DEMO_STATE="${RZ_DEMO_STATE:-${TMPDIR:-/tmp}/rhizome-demo-01}"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

LEASE=60

cmd_setup() {
  rz_start
  local ref args
  args="$(jq -nc --arg root "$STATE/project" '{project_root:$root}')"
  ref="$(rz_result open_project "$args" | jq -r .project_ref)"
  state_set ref "$ref"
  args="$(jq -nc --arg ref "$ref" '{project_ref:$ref, type:"task", title:"Implement request rate limiter", status:"ready", priority:"high"}')"
  rz_result create_issue "$args" >/dev/null
  say "one repository, one tracker, two agent sessions"
  ok "rhizome-mcp serving over MCP — ISSUE-1 \"Implement request rate limiter\" is ready"
}

claim_args() {
  jq -nc --arg ref "$(state_get ref)" --argjson lease "$LEASE" \
    '{project_ref:$ref, issue_id:"ISSUE-1", lease_seconds:$lease}'
}

cmd_claim_a() {
  local out
  run "agent-a:" "claim_issue ISSUE-1 (lease ${LEASE}s)"
  out="$(rz_result claim_issue "$(claim_args)")"
  state_set attempt_a "$(jq -r .attempt.id <<<"$out")"
  state_set token_a "$(jq -r .lease_token <<<"$out")"
  ok "attempt active — renewable lease, expires in ${LEASE}s"
}

cmd_claim_b() {
  local err
  run "agent-b:" "claim_issue ISSUE-1"
  err="$(rz_error claim_issue "$(claim_args)")"
  bad "$err"
  ok "one active attempt per issue — enforced by the database, not by convention"
}

cmd_checkpoint() {
  local args
  run "agent-a:" "save_attempt_note (checkpoint)"
  args="$(jq -nc --arg ref "$(state_get ref)" --arg at "$(state_get attempt_a)" --arg tok "$(state_get token_a)" \
    '{project_ref:$ref, attempt_id:$at, lease_token:$tok, kind:"checkpoint",
      content:"Token bucket implemented in limiter.go; config wiring and tests remain.",
      next_steps:["wire limiter config","add integration test"]}')"
  rz_result save_attempt_note "$args" >/dev/null
  ok "checkpoint saved: \"token bucket done; config wiring and tests remain\""
  echo
  say "then agent-a's process dies — crash, sleep, context limit. no cleanup runs."
}

cmd_board() {
  run "\$" "rhizome-mcp board"
  rz_board | awk '/^status_counts/,/^$/' | column -t
  rz_board | awk '/^active_attempts/,/^$/' | cut -f1-4 | column -t
  ok "in_progress is not stored — it is derived from the live lease"
}

cmd_reclaim() {
  local out ctx args
  run "agent-b:" "claim_issue ISSUE-1"
  out="$(rz_result claim_issue "$(claim_args)")"
  ok "claimed — the expired lease released the issue on its own"
  run "agent-b:" "get_work_context ISSUE-1"
  args="$(jq -nc --arg ref "$(state_get ref)" '{project_ref:$ref, issue_id:"ISSUE-1"}')"
  ctx="$(rz_result get_work_context "$args")"
  ok "resumes from agent-a's checkpoint:"
  jq -re '"    \"" + .checkpoint.content + "\""' <<<"$ctx"
  jq -r '.checkpoint.next_steps[]? | "    next: " + .' <<<"$ctx"
  echo
  say "no orphaned lock, no lost work. that is the whole point."
}

cmd_cleanup() { rz_stop; }

cmd_all() {
  cmd_setup
  cmd_claim_a
  cmd_claim_b
  cmd_checkpoint
  cmd_board
  say "waiting ${LEASE}s + 5s for the lease to expire..."
  sleep $((LEASE + 5))
  cmd_reclaim
  cmd_cleanup
}

case "${1:-all}" in
  setup) cmd_setup ;;
  claim-a) cmd_claim_a ;;
  claim-b) cmd_claim_b ;;
  checkpoint) cmd_checkpoint ;;
  board) cmd_board ;;
  reclaim) cmd_reclaim ;;
  cleanup) cmd_cleanup ;;
  all) cmd_all ;;
  *) echo "usage: $0 {setup|claim-a|claim-b|checkpoint|board|reclaim|cleanup|all}" >&2; exit 2 ;;
esac
