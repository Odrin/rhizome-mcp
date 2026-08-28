#!/usr/bin/env bash
# Scenario C: the status board on a realistically busy project. Seeds an epic
# with tasks, two live leased attempts (one with reservations), and a blocked
# issue, then renders the board (terminal, and an HTML snapshot on request).
set -euo pipefail
export RZ_DEMO_STATE="${RZ_DEMO_STATE:-${TMPDIR:-/tmp}/rhizome-demo-03}"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

cmd_setup() {
  rz_start
  local ref args
  args="$(jq -nc --arg root "$STATE/project" '{project_root:$root}')"
  ref="$(rz_result open_project "$args" | jq -r .project_ref)"
  state_set ref "$ref"
  args="$(jq -nc --arg ref "$ref" '{
    project_ref: $ref,
    idempotency_key: "demo-board-seed",
    issues: [
      {ref:"epic", type:"epic", title:"Payments v2", status:"open", priority:"high"},
      {ref:"t1", type:"task", parent_ref:"epic", title:"Add payment provider abstraction", status:"ready", priority:"high"},
      {ref:"t2", type:"task", parent_ref:"epic", title:"Rate-limit webhook retries", status:"ready", priority:"medium"},
      {ref:"t3", type:"task", parent_ref:"epic", title:"Migrate invoices table", status:"blocked", priority:"high",
       blocked_reason:"waiting for the agreed maintenance window"},
      {ref:"t4", type:"task", parent_ref:"epic", title:"Update payment API docs", status:"ready", priority:"low"},
      {ref:"t5", type:"bug", title:"Fix rounding error in refunds", status:"ready", priority:"critical"}
    ],
    relations: [
      {source_ref:"t1", target_ref:"t2", type:"blocks"}
    ],
    decisions: []
  }')"
  rz_result apply_issue_plan "$args" >/dev/null
  args="$(jq -nc --arg ref "$ref" \
    '{project_ref:$ref, issue_id:"ISSUE-2", lease_seconds:900,
      resources:[{kind:"directory",path:"internal/payments"}]}')"
  rz_result claim_issue "$args" >/dev/null
  args="$(jq -nc --arg ref "$ref" '{project_ref:$ref, issue_id:"ISSUE-6", lease_seconds:900}')"
  rz_result claim_issue "$args" >/dev/null
  ok "seeded: 1 epic, 5 issues, 2 live leases, 1 reservation, 1 blocked"
}

cmd_board() {
  run "\$" "rhizome-mcp board"
  local out
  out="$(rz_board)"
  awk '/^status_counts/,/^$/'      <<<"$out" | column -t
  awk '/^active_attempts/,/^$/'    <<<"$out" | cut -f1-4 | column -t
  awk '/^active_reservations/,/^$/'<<<"$out" | cut -f1,2,4,5 | column -t
  awk '/^blocked_issues/,/^$/'     <<<"$out" | column -t -s $'\t'
}

cmd_html() {
  local out="${2:-board.html}" abs
  case "$out" in /*) abs="$out" ;; *) abs="$PWD/$out" ;; esac
  (cd "$STATE/project" && "$BIN" board --data-root "$STATE/data" --output "$abs")
  ok "self-contained HTML snapshot written to $out"
}

cmd_cleanup() { rz_stop; }

cmd_all() {
  cmd_setup
  cmd_board
  cmd_cleanup
}

case "${1:-all}" in
  setup) cmd_setup ;;
  board) cmd_board ;;
  html) cmd_html "$@" ;;
  cleanup) cmd_cleanup ;;
  all) cmd_all ;;
  *) echo "usage: $0 {setup|board|html [PATH]|cleanup|all}" >&2; exit 2 ;;
esac
