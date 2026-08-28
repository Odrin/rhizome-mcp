#!/usr/bin/env bash
# Scenario B: resource reservations. Two agents claim different issues, but
# their work touches the same resources; the second claim fails atomically
# instead of both agents colliding later. Invoked one subcommand at a time by
# tapes/02-reservation-conflict.tape; `all` runs the full flow as a smoke test.
#
# Arguments are built with jq -nc rather than inline escaped JSON: macOS
# bash 3.2 mis-parses nested quotes inside $(...) and brace-expands the
# payload.
set -euo pipefail
export RZ_DEMO_STATE="${RZ_DEMO_STATE:-${TMPDIR:-/tmp}/rhizome-demo-02}"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

cmd_setup() {
  rz_start
  local ref args
  args="$(jq -nc --arg root "$STATE/project" '{project_root:$root}')"
  ref="$(rz_result open_project "$args" | jq -r .project_ref)"
  state_set ref "$ref"
  args="$(jq -nc --arg ref "$ref" '{project_ref:$ref, type:"task", title:"Add users table migration", status:"ready"}')"
  rz_result create_issue "$args" >/dev/null
  args="$(jq -nc --arg ref "$ref" '{project_ref:$ref, type:"task", title:"Seed default admin user", status:"ready"}')"
  rz_result create_issue "$args" >/dev/null
  say "two agents, two different issues — but the same database directory"
  ok "ISSUE-1 \"Add users table migration\" and ISSUE-2 \"Seed default admin user\" are ready"
}

cmd_claim_a() {
  local args out
  run "agent-a:" "claim_issue ISSUE-1 + reserve migrations/ and the dev database"
  args="$(jq -nc --arg ref "$(state_get ref)" \
    '{project_ref:$ref, issue_id:"ISSUE-1", lease_seconds:300,
      resources:[{kind:"directory",path:"migrations"},{kind:"logical",namespace:"db",name:"dev-database"}]}')"
  out="$(rz_result claim_issue "$args")"
  state_set attempt_a "$(jq -r .attempt.id <<<"$out")"
  ok "claimed — reservations are leased with the attempt, all-or-nothing"
}

cmd_claim_b() {
  local args err
  run "agent-b:" "claim_issue ISSUE-2 + reserve migrations/0002_seed.sql"
  args="$(jq -nc --arg ref "$(state_get ref)" \
    '{project_ref:$ref, issue_id:"ISSUE-2", lease_seconds:300,
      resources:[{kind:"file",path:"migrations/0002_seed.sql"}]}')"
  err="$(rz_error claim_issue "$args")"
  bad "$err"
  ok "the whole claim failed atomically — agent-b never starts work it cannot finish"
}

cmd_claim_b_retry() {
  local args
  run "agent-b:" "claim_issue ISSUE-2 + reserve seeds/ instead"
  args="$(jq -nc --arg ref "$(state_get ref)" \
    '{project_ref:$ref, issue_id:"ISSUE-2", lease_seconds:300,
      resources:[{kind:"directory",path:"seeds"}]}')"
  rz_result claim_issue "$args" >/dev/null
  ok "claimed — non-overlapping resources, both agents work in parallel"
  echo
  say "ports, migration dirs, deploy slots: one mutex, leased like everything else"
}

cmd_cleanup() { rz_stop; }

cmd_all() {
  cmd_setup
  cmd_claim_a
  cmd_claim_b
  cmd_claim_b_retry
  cmd_cleanup
}

case "${1:-all}" in
  setup) cmd_setup ;;
  claim-a) cmd_claim_a ;;
  claim-b) cmd_claim_b ;;
  claim-b-retry) cmd_claim_b_retry ;;
  cleanup) cmd_cleanup ;;
  all) cmd_all ;;
  *) echo "usage: $0 {setup|claim-a|claim-b|claim-b-retry|cleanup|all}" >&2; exit 2 ;;
esac
