#!/usr/bin/env bash
# Scenario D: review requests pin an exact target. A request freezes the
# issue version and event position it covers; when the implementation moves
# on, approval is refused and the request can only be superseded into a
# successor pinned to the new target — approving stale content is
# structurally impossible. Invoked one subcommand at a time by
# tapes/04-review-superseded.tape; `all` runs the full flow as a smoke test.
#
# Arguments are built with jq -nc rather than inline escaped JSON: macOS
# bash 3.2 mis-parses nested quotes inside $(...) and brace-expands the
# payload.
set -euo pipefail
export RZ_DEMO_STATE="${RZ_DEMO_STATE:-${TMPDIR:-/tmp}/rhizome-demo-04}"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

cmd_setup() {
  rz_start
  local ref args out
  args="$(jq -nc --arg root "$STATE/project" '{project_root:$root}')"
  ref="$(rz_result open_project "$args" | jq -r .project_ref)"
  state_set ref "$ref"
  args="$(jq -nc --arg ref "$ref" '{project_ref:$ref, type:"task", title:"Refactor auth middleware", status:"ready"}')"
  rz_result create_issue "$args" >/dev/null
  args="$(jq -nc --arg ref "$ref" '{project_ref:$ref, issue_id:"ISSUE-1", lease_seconds:300}')"
  out="$(rz_result claim_issue "$args")"
  args="$(jq -nc --arg ref "$ref" --arg at "$(jq -r .attempt.id <<<"$out")" --arg tok "$(jq -r .lease_token <<<"$out")" \
    '{project_ref:$ref, attempt_id:$at, lease_token:$tok, outcome:"completed", target_issue_status:"review",
      result_summary:"Middleware refactored; ready for review."}')"
  out="$(rz_result finish_attempt "$args")"
  state_set issue_version "$(jq -r .issue.version <<<"$out")"
  state_set event_id "$(jq -r .latest_event_id <<<"$out")"
  say "an implementation just landed and wants review"
  ok "ISSUE-1 \"Refactor auth middleware\" is in review at version $(state_get issue_version)"
}

cmd_request() {
  local args out
  run "agent-a:" "create_review_request ISSUE-1 (pin version $(state_get issue_version), event $(state_get event_id))"
  args="$(jq -nc --arg ref "$(state_get ref)" \
    --argjson v "$(state_get issue_version)" --argjson e "$(state_get event_id)" \
    '{project_ref:$ref, issue_id:"ISSUE-1", target_issue_version:$v, target_event_id:$e}')"
  out="$(rz_result create_review_request "$args")"
  state_set request_id "$(jq -r '.request.id // .id' <<<"$out")"
  state_set request_version "$(jq -r '.request.version // .version' <<<"$out")"
  ok "review request open — frozen to exactly this version of the work"
}

cmd_edit() {
  local args
  run "agent-b:" "update_issue ISSUE-1 (the implementation moves on)"
  args="$(jq -nc --arg ref "$(state_get ref)" --argjson v "$(state_get issue_version)" \
    '{project_ref:$ref, issue_id:"ISSUE-1", expected_version:$v,
      changes:{description:"Switched to context-scoped middleware chain after the review request was opened."}}')"
  rz_result update_issue "$args" >/dev/null
  ok "issue is now version $(( $(state_get issue_version) + 1 )) — the reviewed snapshot no longer matches"
}

cmd_approve_fails() {
  local args out err
  run "reviewer:" "claim_issue ISSUE-1 + finish_attempt (approved)"
  args="$(jq -nc --arg ref "$(state_get ref)" '{project_ref:$ref, issue_id:"ISSUE-1", lease_seconds:300}')"
  out="$(rz_result claim_issue "$args")"
  args="$(jq -nc --arg ref "$(state_get ref)" --arg at "$(jq -r .attempt.id <<<"$out")" --arg tok "$(jq -r .lease_token <<<"$out")" \
    '{project_ref:$ref, attempt_id:$at, lease_token:$tok, outcome:"completed", review_outcome:"approved",
      result_summary:"Looks good."}')"
  err="$(rz_error finish_attempt "$args")"
  bad "$err"
  ok "the approval is refused — the pinned snapshot no longer matches the issue"
}

cmd_replace() {
  local args out current_version latest_event
  run "reviewer:" "replace_review_request (supersede, re-pin to the new version)"
  current_version="$(( $(state_get issue_version) + 1 ))"
  latest_event="$(rz_result get_project "$(jq -nc --arg ref "$(state_get ref)" '{project_ref:$ref}')" | jq -r .latest_event_id)"
  args="$(jq -nc --arg ref "$(state_get ref)" --arg id "$(state_get request_id)" \
    --argjson rv "$(state_get request_version)" --argjson v "$current_version" --argjson e "$latest_event" \
    '{project_ref:$ref, predecessor_request_id:$id, predecessor_expected_version:$rv,
      target_issue_version:$v, target_event_id:$e, idempotency_key:"demo-replace-1"}')"
  out="$(rz_result replace_review_request "$args")"
  ok "predecessor: $(jq -r .predecessor.status <<<"$out") — successor open, pinned to version $current_version"
  echo
  say "a review can supersede and re-pin. it can never approve what changed underneath it."
}

cmd_cleanup() { rz_stop; }

cmd_all() {
  cmd_setup
  cmd_request
  cmd_edit
  cmd_approve_fails
  local out
  cmd_replace
  out="$(rz_result get_review_request "$(jq -nc --arg ref "$(state_get ref)" --arg id "$(state_get request_id)" '{project_ref:$ref, review_request_id:$id}')")"
  if [ "$(jq -r '.request.status // .status' <<<"$out")" != "superseded" ]; then
    bad "expected predecessor status superseded, got: $(jq -r '.request.status // .status' <<<"$out")"
    cmd_cleanup
    exit 1
  fi
  cmd_cleanup
}

case "${1:-all}" in
  setup) cmd_setup ;;
  request) cmd_request ;;
  edit) cmd_edit ;;
  approve-fails) cmd_approve_fails ;;
  replace) cmd_replace ;;
  cleanup) cmd_cleanup ;;
  all) cmd_all ;;
  *) echo "usage: $0 {setup|request|edit|approve-fails|replace|cleanup|all}" >&2; exit 2 ;;
esac
