-- ISSUE-173: purpose-scoped review approvals.
--
-- docs/02 §17.5 fixes what a review_approval requirement means -- a named
-- purpose that must have a matching immutable approval record -- and defers
-- the persistence and creation mechanism to this task. Three pieces of state
-- follow from the locked design:
--
--   1. Review targets and review requests carry the purposes they cover.
--   2. An immutable, purpose-scoped approval record is what satisfies a
--      review_approval requirement.
--   3. Rows written before this migration keep working: they cover the
--      implementation purpose and freeze an empty review gate snapshot, so
--      the old behaviour stays valid whenever no policy matches.
--
-- The review-target snapshot table itself already exists (migration 009);
-- only the backfill for targets that predate its first writer is here.

-- Purposes are a unique, sorted list of 1-10 normalized keys. SQLite enforces
-- the container and its bounds; uniqueness, sortedness, and key normalization
-- are validated in the domain before the write -- the same split migration 011
-- uses for reservation overlap, since neither is expressible as a constraint.
--
-- The default is permanent, not just a backfill value: docs/02 §17.5 makes
-- [implementation] the compatibility default for callers (and for logical
-- import, which inserts review rows column-by-column) that never name a
-- purpose at all.
ALTER TABLE review_targets ADD COLUMN purposes_json TEXT NOT NULL DEFAULT '["implementation"]'
    CHECK (json_valid(purposes_json)
        AND json_type(purposes_json) = 'array'
        AND json_array_length(purposes_json) BETWEEN 1 AND 10);

ALTER TABLE review_requests ADD COLUMN purposes_json TEXT NOT NULL DEFAULT '["implementation"]'
    CHECK (json_valid(purposes_json)
        AND json_type(purposes_json) = 'array'
        AND json_array_length(purposes_json) BETWEEN 1 AND 10);

-- One row per purpose granted by one approved review request. The record is
-- append-only and never updated: docs/02 §17.5 requires an *immutable*
-- approval record, and re-approving at a later issue version writes a new row
-- against a new request rather than mutating this one.
--
-- target_id doubles as the snapshot reference: review_target_gate_snapshots is
-- keyed by target_id, so the row it points at is the exact frozen
-- review_approval requirement set this approval was granted against (docs/02
-- §17.6). target_issue_version and target_event_id are denormalized from that
-- target the same way review_requests already denormalizes them, so a gate
-- evaluation can apply the existing staleness rule -- exact issue version plus
-- "did the reviewed work change after target_event_id" -- without re-joining
-- the target on every check.
CREATE TABLE review_approvals (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    issue_id TEXT NOT NULL REFERENCES issues(id),
    target_id TEXT NOT NULL REFERENCES review_targets(id),
    request_id TEXT NOT NULL REFERENCES review_requests(id),
    attempt_id TEXT NOT NULL REFERENCES work_attempts(id),
    purpose TEXT NOT NULL CHECK (length(trim(purpose)) > 0),
    target_issue_version INTEGER NOT NULL CHECK (target_issue_version >= 1),
    target_event_id INTEGER NOT NULL CHECK (target_event_id >= 0),
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at TEXT NOT NULL
) STRICT;

-- A request grants each of its purposes exactly once. Only an approved
-- request writes rows at all, and review_outcomes already admits one outcome
-- per request, so this index is the second, database-level guard against a
-- retried approval double-granting a purpose.
CREATE UNIQUE INDEX idx_review_approvals_request_purpose
ON review_approvals(request_id, purpose);

-- The gate lookup at approve_review and complete_work_to_done: which purposes
-- does this issue hold an approval for, at which target version.
CREATE INDEX idx_review_approvals_issue_purpose
ON review_approvals(issue_id, purpose, target_issue_version);

CREATE TRIGGER review_approvals_immutable_update
BEFORE UPDATE ON review_approvals
BEGIN
    SELECT RAISE(ABORT, 'review approvals are immutable');
END;

CREATE TRIGGER review_approvals_immutable_delete
BEFORE DELETE ON review_approvals
BEGIN
    SELECT RAISE(ABORT, 'review approvals are never deleted');
END;

-- Backfill: every review target that predates the first snapshot writer
-- freezes an empty review_approval requirement set, so approving an in-flight
-- request keeps working across the upgrade and reading a target snapshot stays
-- an unconditional read rather than a "missing means legacy" branch.
--
-- The fingerprint is a documented sentinel, not a hash. A real fingerprint is
-- SHA-256 over the canonical snapshot payload, which includes the issue
-- version, so it differs per target and SQLite cannot compute it. Nothing
-- compares fingerprints for equality -- the column is snapshot identity for
-- audit -- and no SHA-256 collides with all zeroes, so the sentinel stays
-- distinguishable from any genuinely computed snapshot.
INSERT INTO review_target_gate_snapshots(
    target_id, requirements_json, source_policies_json, fingerprint, issue_version, created_at
)
SELECT
    review_targets.id,
    '[]',
    '[]',
    '0000000000000000000000000000000000000000000000000000000000000000',
    review_targets.issue_version,
    review_targets.created_at
FROM review_targets
WHERE NOT EXISTS (
    SELECT 1 FROM review_target_gate_snapshots WHERE target_id = review_targets.id
);
