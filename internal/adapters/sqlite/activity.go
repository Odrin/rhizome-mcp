package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/pagination"
	"rhizome-mcp/internal/ports"
)

type ActivityRepository struct {
	db *DB
}

type activityCursor struct {
	OccurredAt string `json:"occurred_at"`
	TypeRank   int    `json:"type_rank"`
	SortID     string `json:"sort_id"`
}

var activityCursorCodec = pagination.NewCodec[activityCursor](0)

var _ ports.ActivityRepository = (*ActivityRepository)(nil)

func NewActivityRepository(database *DB) (*ActivityRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "activity database is required", false)
	}
	return &ActivityRepository{db: database}, nil
}

func (repository *ActivityRepository) GetIssueActivity(ctx context.Context, command ports.GetIssueActivityCommand) (domain.IssueActivity, error) {
	input, err := validateActivityInput(command.Input)
	if err != nil {
		return domain.IssueActivity{}, err
	}

	var after *activityCursor
	if input.Cursor != "" {
		decoded, err := activityCursorCodec.Decode(input.Cursor)
		if err != nil || decoded.TypeRank < 1 || decoded.TypeRank > activityMaxRank || decoded.OccurredAt == "" || !isValidActivityCursorSortID(decoded.TypeRank, decoded.SortID) {
			if err == nil {
				err = errors.New("activity cursor payload is invalid")
			}
			return domain.IssueActivity{}, activityCursorError(err)
		}
		if _, err := parseIssueTimestamp("occurred_at", decoded.OccurredAt); err != nil {
			return domain.IssueActivity{}, activityCursorError(errors.New("activity cursor payload is invalid"))
		}
		after = &decoded
	}

	var result domain.IssueActivity
	err = repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		canonicalIssueID, err := resolveActivityIssueID(ctx, query, input.IssueID)
		if err != nil {
			return err
		}

		arms, args, err := buildActivityUnionArms(input.Types, canonicalIssueID)
		if err != nil {
			return err
		}
		if after != nil {
			args = append(args, after.OccurredAt, after.OccurredAt, after.TypeRank, after.TypeRank, after.SortID)
		}
		statement := "SELECT entity_type, entity_id, occurred_at, type_rank, sort_id FROM (" + strings.Join(arms, " UNION ALL ") + ") AS activity"
		if after != nil {
			statement += " WHERE occurred_at < ? OR (occurred_at = ? AND (type_rank > ? OR (type_rank = ? AND sort_id > ?)))"
		}
		statement += " ORDER BY occurred_at DESC, type_rank ASC, sort_id ASC LIMIT ?"
		args = append(args, input.Limit+1)

		rows, err := query.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		descriptors := make([]activityDescriptor, 0, input.Limit+1)
		for rows.Next() {
			descriptor, err := scanActivityDescriptor(rows)
			if err != nil {
				rows.Close()
				return err
			}
			descriptors = append(descriptors, descriptor)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return activityCorrupt(err)
		}
		if err := rows.Close(); err != nil {
			return activityCorrupt(err)
		}

		if len(descriptors) > input.Limit {
			result.HasMore = true
			descriptors = descriptors[:input.Limit]
		}
		items := make([]domain.ActivityItem, 0, len(descriptors))
		for _, descriptor := range descriptors {
			item, err := loadActivityItem(ctx, query, canonicalIssueID, descriptor)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		if len(items) == 0 {
			result.Items = []domain.ActivityItem{}
		} else {
			result.Items = items
		}
		if result.HasMore {
			last := descriptors[len(descriptors)-1]
			cursor, err := activityCursorCodec.Encode(activityCursor{OccurredAt: last.OccurredAtText, TypeRank: last.TypeRank, SortID: last.SortID})
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode activity cursor", false)
			}
			result.NextCursor = &cursor
		}
		return nil
	})
	if err != nil {
		return domain.IssueActivity{}, err
	}
	if result.Items == nil {
		result.Items = []domain.ActivityItem{}
	}
	return domain.CloneIssueActivity(result), nil
}

type activityDescriptor struct {
	EntityType     domain.ActivityEntityType
	EntityID       string
	OccurredAtText string
	OccurredAt     time.Time
	TypeRank       int
	SortID         string
}

func resolveActivityIssueID(ctx context.Context, query Queryer, issueID string) (string, error) {
	identifier, err := domain.ParseIssueIdentifier(issueID)
	if err != nil {
		return "", err
	}
	var id string
	switch identifier.Kind {
	case domain.IssueIdentifierInternalID:
		err = query.QueryRowContext(ctx, `SELECT id FROM issues WHERE id = ?`, identifier.Value).Scan(&id)
	case domain.IssueIdentifierDisplayID:
		err = query.QueryRowContext(ctx, `SELECT id FROM issues WHERE sequence_no = ?`, identifier.SequenceNo).Scan(&id)
	default:
		return "", domain.NewError(domain.CodeInvalidArgument, "issue identifier is invalid", false,
			domain.Detail{Field: "issue_id", Code: "INVALID_IDENTIFIER"})
	}
	if err == sql.ErrNoRows {
		return "", domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// activitySortIDKind identifies how a spec's sort_id column is validated and
// (for the cursor decoder) how its value is shaped.
type activitySortIDKind int

const (
	activitySortIDULID activitySortIDKind = iota
	activitySortIDEventSequence
)

// activityEntitySpec is the single source of truth for one activity entity
// kind: its filter category, discriminated payload type, stable rank
// (persisted in opaque pagination cursors), SQL union arm, sort-id shape, and
// loader. Every switch that used to enumerate entity kinds separately
// (buildActivityUnionArms, activityCategoryRank, expectedActivityEntityType,
// the cursor/scan rank bound, isValidActivityCursorSortID, loadActivityItem)
// is now driven from activityRegistry below.
//
// Rank is append-only and must never be renumbered: it is embedded in the SQL
// union arm text and round-trips through client-held cursors. New entity
// kinds must append new specs with the next unused rank (currently 10, ...)
// in registration order; existing ranks 1-8 are permanently reserved for the
// specs below.
type activityEntitySpec struct {
	Category   domain.ActivityCategory
	EntityType domain.ActivityEntityType
	Rank       int
	Arm        string
	SortIDKind activitySortIDKind
	Load       func(ctx context.Context, query Queryer, item *domain.ActivityItem, entityID string) error
}

var activityRegistry = []activityEntitySpec{
	{
		Category:   domain.ActivityCategoryComments,
		EntityType: domain.ActivityEntityTypeComment,
		Rank:       1,
		Arm:        `SELECT 'comment' AS entity_type, comments.id AS entity_id, comments.created_at AS occurred_at, 1 AS type_rank, comments.id AS sort_id FROM comments WHERE comments.issue_id = ?`,
		SortIDKind: activitySortIDULID,
		Load: func(ctx context.Context, query Queryer, item *domain.ActivityItem, entityID string) error {
			comment, err := loadActivityComment(ctx, query, entityID)
			if err != nil {
				return err
			}
			item.Comment = &comment
			return nil
		},
	},
	{
		Category:   domain.ActivityCategoryDecisions,
		EntityType: domain.ActivityEntityTypeDecision,
		Rank:       2,
		Arm:        `SELECT 'decision' AS entity_type, decisions.id AS entity_id, decisions.created_at AS occurred_at, 2 AS type_rank, decisions.id AS sort_id FROM decisions WHERE decisions.issue_id = ?`,
		SortIDKind: activitySortIDULID,
		Load: func(ctx context.Context, query Queryer, item *domain.ActivityItem, entityID string) error {
			decision, err := loadActivityDecision(ctx, query, entityID)
			if err != nil {
				return err
			}
			item.Decision = &decision
			return nil
		},
	},
	{
		Category:   domain.ActivityCategoryReviews,
		EntityType: domain.ActivityEntityTypeReview,
		Rank:       3,
		Arm:        `SELECT 'review' AS entity_type, review_requests.id AS entity_id, review_requests.created_at AS occurred_at, 3 AS type_rank, review_requests.id AS sort_id FROM review_requests WHERE review_requests.issue_id = ?`,
		SortIDKind: activitySortIDULID,
		Load: func(ctx context.Context, query Queryer, item *domain.ActivityItem, entityID string) error {
			review, err := loadActivityReview(ctx, query, entityID)
			if err != nil {
				return err
			}
			item.Review = &review
			return nil
		},
	},
	{
		Category:   domain.ActivityCategoryAttempts,
		EntityType: domain.ActivityEntityTypeAttempt,
		Rank:       4,
		Arm:        `SELECT 'attempt' AS entity_type, work_attempts.id AS entity_id, work_attempts.started_at AS occurred_at, 4 AS type_rank, work_attempts.id AS sort_id FROM work_attempts WHERE work_attempts.issue_id = ?`,
		SortIDKind: activitySortIDULID,
		Load: func(ctx context.Context, query Queryer, item *domain.ActivityItem, entityID string) error {
			attempt, err := loadActivityAttempt(ctx, query, entityID)
			if err != nil {
				return err
			}
			item.Attempt = &attempt
			return nil
		},
	},
	{
		Category:   domain.ActivityCategoryAttemptNotes,
		EntityType: domain.ActivityEntityTypeAttemptNote,
		Rank:       5,
		Arm:        `SELECT 'attempt_note' AS entity_type, attempt_notes.id AS entity_id, attempt_notes.created_at AS occurred_at, 5 AS type_rank, attempt_notes.id AS sort_id FROM attempt_notes JOIN work_attempts ON work_attempts.id = attempt_notes.attempt_id WHERE work_attempts.issue_id = ?`,
		SortIDKind: activitySortIDULID,
		Load: func(ctx context.Context, query Queryer, item *domain.ActivityItem, entityID string) error {
			note, err := loadActivityAttemptNote(ctx, query, entityID)
			if err != nil {
				return err
			}
			item.AttemptNote = &note
			return nil
		},
	},
	{
		Category:   domain.ActivityCategoryEvents,
		EntityType: domain.ActivityEntityTypeEvent,
		Rank:       6,
		Arm:        `SELECT 'event' AS entity_type, CAST(issue_events.id AS TEXT) AS entity_id, issue_events.created_at AS occurred_at, 6 AS type_rank, printf('%020d', issue_events.id) AS sort_id FROM issue_events WHERE issue_events.issue_id = ?`,
		SortIDKind: activitySortIDEventSequence,
		Load: func(ctx context.Context, query Queryer, item *domain.ActivityItem, entityID string) error {
			event, err := loadActivityEvent(ctx, query, entityID)
			if err != nil {
				return err
			}
			item.Event = &event
			return nil
		},
	},
	{
		Category:   domain.ActivityCategoryArtifacts,
		EntityType: domain.ActivityEntityTypeArtifact,
		Rank:       7,
		Arm:        `SELECT 'artifact' AS entity_type, artifacts.id AS entity_id, artifacts.created_at AS occurred_at, 7 AS type_rank, artifacts.id AS sort_id FROM artifacts WHERE artifacts.issue_id = ?`,
		SortIDKind: activitySortIDULID,
		Load: func(ctx context.Context, query Queryer, item *domain.ActivityItem, entityID string) error {
			artifact, err := loadActivityArtifact(ctx, query, entityID)
			if err != nil {
				return err
			}
			item.Artifact = &artifact
			return nil
		},
	},
	{
		Category:   domain.ActivityCategoryGateEvidence,
		EntityType: domain.ActivityEntityTypeGateEvidence,
		Rank:       8,
		// occurred_at uses updated_at, not created_at: submit_gate_evidence
		// is an upsert (ISSUE-171), and a replacement is itself new activity.
		Arm:        `SELECT 'gate_evidence' AS entity_type, gate_evidence.id AS entity_id, gate_evidence.updated_at AS occurred_at, 8 AS type_rank, gate_evidence.id AS sort_id FROM gate_evidence WHERE gate_evidence.issue_id = ?`,
		SortIDKind: activitySortIDULID,
		Load: func(ctx context.Context, query Queryer, item *domain.ActivityItem, entityID string) error {
			evidence, err := loadAttemptEvidenceByID(ctx, query, entityID)
			if err != nil {
				return err
			}
			item.GateEvidence = &evidence
			return nil
		},
	},
	{
		Category:   domain.ActivityCategoryReservations,
		EntityType: domain.ActivityEntityTypeReservation,
		Rank:       9,
		// occurred_at uses COALESCE(released_at, created_at): a reservation
		// moves in the timeline when it is released, mirroring how
		// gate_evidence uses updated_at.
		Arm:        `SELECT 'reservation' AS entity_type, resource_reservations.id AS entity_id, COALESCE(resource_reservations.released_at, resource_reservations.created_at) AS occurred_at, 9 AS type_rank, resource_reservations.id AS sort_id FROM resource_reservations WHERE resource_reservations.issue_id = ?`,
		SortIDKind: activitySortIDULID,
		Load: func(ctx context.Context, query Queryer, item *domain.ActivityItem, entityID string) error {
			reservation, err := loadActivityReservation(ctx, query, entityID)
			if err != nil {
				return err
			}
			summary := domain.SummarizeReservation(reservation)
			item.Reservation = &summary
			return nil
		},
	},
}

var (
	activityByCategory   = make(map[domain.ActivityCategory]activityEntitySpec, len(activityRegistry))
	activityByRank       = make(map[int]activityEntitySpec, len(activityRegistry))
	activityByEntityType = make(map[domain.ActivityEntityType]activityEntitySpec, len(activityRegistry))
	activityMaxRank      int
)

func init() {
	for _, spec := range activityRegistry {
		activityByCategory[spec.Category] = spec
		activityByRank[spec.Rank] = spec
		activityByEntityType[spec.EntityType] = spec
		if spec.Rank > activityMaxRank {
			activityMaxRank = spec.Rank
		}
	}
}

func buildActivityUnionArms(types []domain.ActivityCategory, issueID string) ([]string, []any, error) {
	if len(types) == 0 {
		types = append([]domain.ActivityCategory(nil), domain.AllActivityCategories...)
	}
	ordered := make([]domain.ActivityCategory, 0, len(types))
	seen := make(map[domain.ActivityCategory]struct{}, len(types))
	for _, category := range types {
		if _, exists := seen[category]; exists {
			continue
		}
		seen[category] = struct{}{}
		ordered = append(ordered, category)
	}
	ordered = sortActivityCategories(ordered)
	arms := make([]string, 0, len(ordered))
	args := make([]any, 0, len(ordered)+1)
	for _, category := range ordered {
		spec, ok := activityByCategory[category]
		if !ok {
			return nil, nil, domain.NewError(domain.CodeInvalidArgument, "activity category is invalid", false)
		}
		arms = append(arms, spec.Arm)
		args = append(args, issueID)
	}
	return arms, args, nil
}

func sortActivityCategories(values []domain.ActivityCategory) []domain.ActivityCategory {
	ordered := append([]domain.ActivityCategory(nil), values...)
	for index := 0; index < len(ordered)-1; index++ {
		for j := index + 1; j < len(ordered); j++ {
			if activityCategoryRank(ordered[index]) > activityCategoryRank(ordered[j]) {
				ordered[index], ordered[j] = ordered[j], ordered[index]
			}
		}
	}
	return ordered
}

func activityCategoryRank(category domain.ActivityCategory) int {
	return activityByCategory[category].Rank
}

func scanActivityDescriptor(scanner scanner) (activityDescriptor, error) {
	var entityType, entityID, occurredAt string
	var typeRank int
	var sortID string
	if err := scanner.Scan(&entityType, &entityID, &occurredAt, &typeRank, &sortID); err != nil {
		return activityDescriptor{}, activityCorrupt(err)
	}
	if entityType == "" {
		return activityDescriptor{}, activityCorruptField(nil, "entity_type", "REQUIRED")
	}
	entity := domain.ActivityEntityType(entityType)
	if !entity.Valid() {
		return activityDescriptor{}, activityCorruptField(nil, "entity_type", "INVALID_ENUM")
	}
	if typeRank < 1 || typeRank > activityMaxRank {
		return activityDescriptor{}, activityCorruptField(nil, "type_rank", "INVALID_VALUE")
	}
	if entity != expectedActivityEntityType(typeRank) {
		return activityDescriptor{}, activityCorruptField(nil, "entity_type", "MISMATCH")
	}
	if entityID == "" {
		return activityDescriptor{}, activityCorruptField(nil, "entity_id", "REQUIRED")
	}
	if occurredAt == "" {
		return activityDescriptor{}, activityCorruptField(nil, "occurred_at", "REQUIRED")
	}
	parsed, err := parseIssueTimestamp("occurred_at", occurredAt)
	if err != nil {
		return activityDescriptor{}, err
	}
	if !isValidActivityCursorSortID(typeRank, sortID) {
		return activityDescriptor{}, activityCorruptField(nil, "sort_id", "INVALID_VALUE")
	}
	if entity != domain.ActivityEntityTypeEvent {
		if entityID != sortID {
			return activityDescriptor{}, activityCorruptField(nil, "entity_id", "MISMATCH")
		}
	} else {
		parsedID, err := strconv.ParseInt(entityID, 10, 64)
		if err != nil || parsedID <= 0 || strconv.FormatInt(parsedID, 10) != entityID {
			return activityDescriptor{}, activityCorruptField(nil, "entity_id", "INVALID_VALUE")
		}
		if sortID != fmt.Sprintf("%020d", parsedID) {
			return activityDescriptor{}, activityCorruptField(nil, "sort_id", "INVALID_VALUE")
		}
	}
	return activityDescriptor{EntityType: entity, EntityID: entityID, OccurredAtText: occurredAt, OccurredAt: parsed, TypeRank: typeRank, SortID: sortID}, nil
}

func expectedActivityEntityType(rank int) domain.ActivityEntityType {
	return activityByRank[rank].EntityType
}

func isValidActivityCursorSortID(typeRank int, value string) bool {
	if value == "" {
		return false
	}
	spec, ok := activityByRank[typeRank]
	if !ok {
		return false
	}
	if spec.SortIDKind == activitySortIDEventSequence {
		if len(value) != 20 {
			return false
		}
		for _, char := range value {
			if char < '0' || char > '9' {
				return false
			}
		}
		return true
	}
	_, err := ids.ParseStrict(value)
	return err == nil
}

func loadActivityItem(ctx context.Context, query Queryer, issueID string, descriptor activityDescriptor) (domain.ActivityItem, error) {
	item := domain.ActivityItem{EntityType: descriptor.EntityType, EntityID: descriptor.EntityID, IssueID: issueID, OccurredAt: descriptor.OccurredAt}
	spec, ok := activityByEntityType[descriptor.EntityType]
	if !ok {
		return domain.ActivityItem{}, activityCorruptField(nil, "entity_type", "INVALID_ENUM")
	}
	if err := spec.Load(ctx, query, &item, descriptor.EntityID); err != nil {
		return domain.ActivityItem{}, err
	}
	if err := domain.ValidateActivityItem(item); err != nil {
		return domain.ActivityItem{}, activityCorrupt(err)
	}
	return item, nil
}

func loadActivityComment(ctx context.Context, query Queryer, id string) (domain.Comment, error) {
	comment, err := scanActivityComment(query.QueryRowContext(ctx, `SELECT id, issue_id, content, created_by_session_id, author_label, created_at, edited_at FROM comments WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Comment{}, activityCorrupt(err)
		}
		return domain.Comment{}, err
	}
	return comment, nil
}

func scanActivityComment(scanner scanner) (domain.Comment, error) {
	var (
		id, issueID, content, createdAt  string
		sessionID, authorLabel, editedAt sql.NullString
	)
	if err := scanner.Scan(&id, &issueID, &content, &sessionID, &authorLabel, &createdAt, &editedAt); err != nil {
		if err == sql.ErrNoRows {
			return domain.Comment{}, err
		}
		return domain.Comment{}, activityCorrupt(err)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.Comment{}, activityCorruptField(err, "id", "INVALID_ULID")
	}
	if _, err := ids.ParseStrict(issueID); err != nil {
		return domain.Comment{}, activityCorruptField(err, "issue_id", "INVALID_ULID")
	}
	if err := domain.ValidateText("content", content, domain.MaxCommentRunes); err != nil {
		return domain.Comment{}, activityCorrupt(err)
	}
	if strings.TrimSpace(content) == "" {
		return domain.Comment{}, activityCorruptField(nil, "content", "REQUIRED")
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.Comment{}, err
	}
	edited, err := parseNullableIssueTimestamp("edited_at", editedAt)
	if err != nil {
		return domain.Comment{}, err
	}
	createdBySessionID, err := parseActivityNullableULID("created_by_session_id", sessionID)
	if err != nil {
		return domain.Comment{}, err
	}
	if authorLabel.Valid {
		if err := domain.ValidateText("author_label", authorLabel.String, -1); err != nil {
			return domain.Comment{}, activityCorrupt(err)
		}
	}
	return domain.Comment{ID: id, IssueID: issueID, Content: content, CreatedBySessionID: createdBySessionID, AuthorLabel: nullableStringPointer(authorLabel), CreatedAt: created, EditedAt: edited}, nil
}

func loadActivityDecision(ctx context.Context, query Queryer, id string) (domain.Decision, error) {
	decision, err := scanActivityDecision(query.QueryRowContext(ctx, `SELECT id, issue_id, title, summary, content, status, supersedes_id, created_by_session_id, created_at FROM decisions WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Decision{}, activityCorrupt(err)
		}
		return domain.Decision{}, err
	}
	return decision, nil
}

func scanActivityDecision(scanner scanner) (domain.Decision, error) {
	var (
		id, title, summary, content, status, createdAt string
		issueID, supersedesID, sessionID               sql.NullString
	)
	if err := scanner.Scan(&id, &issueID, &title, &summary, &content, &status, &supersedesID, &sessionID, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return domain.Decision{}, err
		}
		return domain.Decision{}, activityCorrupt(err)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.Decision{}, activityCorruptField(err, "id", "INVALID_ULID")
	}
	decisionIssueID, err := parseActivityNullableULID("issue_id", issueID)
	if err != nil {
		return domain.Decision{}, err
	}
	if err := domain.ValidateText("title", title, domain.MaxTitleRunes); err != nil {
		return domain.Decision{}, activityCorrupt(err)
	}
	if strings.TrimSpace(title) == "" {
		return domain.Decision{}, activityCorruptField(nil, "title", "REQUIRED")
	}
	if err := domain.ValidateText("summary", summary, domain.MaxDecisionSummaryRunes); err != nil {
		return domain.Decision{}, activityCorrupt(err)
	}
	if strings.TrimSpace(summary) == "" {
		return domain.Decision{}, activityCorruptField(nil, "summary", "REQUIRED")
	}
	if err := domain.ValidateText("content", content, domain.MaxDecisionContentRunes); err != nil {
		return domain.Decision{}, activityCorrupt(err)
	}
	decisionStatus := domain.DecisionStatus(status)
	if !decisionStatus.Valid() {
		return domain.Decision{}, activityCorruptField(nil, "status", "INVALID_VALUE")
	}
	supersedes, err := parseActivityNullableULID("supersedes_id", supersedesID)
	if err != nil {
		return domain.Decision{}, err
	}
	session, err := parseActivityNullableULID("created_by_session_id", sessionID)
	if err != nil {
		return domain.Decision{}, err
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.Decision{}, err
	}
	return domain.Decision{ID: id, IssueID: decisionIssueID, Title: title, Summary: summary, Content: content, Status: decisionStatus, SupersedesID: supersedes, CreatedBySessionID: session, CreatedAt: created}, nil
}

func loadActivityAttempt(ctx context.Context, query Queryer, id string) (domain.WorkAttempt, error) {
	attempt, err := scanActivityAttempt(query.QueryRowContext(ctx, `SELECT id, issue_id, session_id, agent_label, kind, status, issue_version_at_start, context_event_id_at_start, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json, failure_reason_code, interruption_reason_code, reason_details FROM work_attempts WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.WorkAttempt{}, activityCorrupt(err)
		}
		return domain.WorkAttempt{}, err
	}
	return attempt, nil
}

func scanActivityAttempt(scanner scanner) (domain.WorkAttempt, error) {
	var (
		id, issueID, kindText, statusText, leaseExpiresAt, startedAt, lastHeartbeatAt                            string
		finishedAt, resultSummary, nextStepsJSON, verificationJSON, failureCode, interruptionCode, reasonDetails sql.NullString
		sessionID, agentLabel                                                                                    sql.NullString
		issueVersionAtStart, contextEventIDAtStart                                                               int64
	)
	if err := scanner.Scan(&id, &issueID, &sessionID, &agentLabel, &kindText, &statusText, &issueVersionAtStart, &contextEventIDAtStart, &leaseExpiresAt, &startedAt, &lastHeartbeatAt, &finishedAt, &resultSummary, &nextStepsJSON, &verificationJSON, &failureCode, &interruptionCode, &reasonDetails); err != nil {
		if err == sql.ErrNoRows {
			return domain.WorkAttempt{}, err
		}
		return domain.WorkAttempt{}, activityCorrupt(err)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.WorkAttempt{}, activityCorruptField(err, "id", "INVALID_ULID")
	}
	if _, err := ids.ParseStrict(issueID); err != nil {
		return domain.WorkAttempt{}, activityCorruptField(err, "issue_id", "INVALID_ULID")
	}
	kind := domain.AttemptKind(kindText)
	if !kind.Valid() {
		return domain.WorkAttempt{}, activityCorruptField(nil, "kind", "INVALID_VALUE")
	}
	status := domain.AttemptStatus(statusText)
	if !status.Valid() {
		return domain.WorkAttempt{}, activityCorruptField(nil, "status", "INVALID_VALUE")
	}
	if issueVersionAtStart < 1 {
		return domain.WorkAttempt{}, activityCorruptField(nil, "issue_version_at_start", "INVALID_VALUE")
	}
	if contextEventIDAtStart < 0 {
		return domain.WorkAttempt{}, activityCorruptField(nil, "context_event_id_at_start", "INVALID_VALUE")
	}
	leaseExpires, err := parseIssueTimestamp("lease_expires_at", leaseExpiresAt)
	if err != nil {
		return domain.WorkAttempt{}, err
	}
	started, err := parseIssueTimestamp("started_at", startedAt)
	if err != nil {
		return domain.WorkAttempt{}, err
	}
	lastHeartbeat, err := parseIssueTimestamp("last_heartbeat_at", lastHeartbeatAt)
	if err != nil {
		return domain.WorkAttempt{}, err
	}
	var finished *time.Time
	if finishedAt.Valid {
		parsedFinished, err := parseIssueTimestamp("finished_at", finishedAt.String)
		if err != nil {
			return domain.WorkAttempt{}, err
		}
		finished = &parsedFinished
	}
	if status == domain.AttemptStatusActive {
		if finished != nil {
			return domain.WorkAttempt{}, activityCorruptField(nil, "finished_at", "INVALID_VALUE")
		}
	} else if finished == nil {
		return domain.WorkAttempt{}, activityCorruptField(nil, "finished_at", "REQUIRED")
	}
	var failureReason *domain.FailureReasonCode
	if failureCode.Valid {
		failureReason, err = parseActivityFailureReason(failureCode)
		if err != nil {
			return domain.WorkAttempt{}, err
		}
		if status != domain.AttemptStatusFailed {
			return domain.WorkAttempt{}, activityCorruptField(nil, "failure_reason_code", "INVALID_VALUE")
		}
	} else if status == domain.AttemptStatusFailed {
		return domain.WorkAttempt{}, activityCorruptField(nil, "failure_reason_code", "REQUIRED")
	}
	var interruptionReason *domain.InterruptionReasonCode
	if interruptionCode.Valid {
		interruptionReason, err = parseActivityInterruptionReason(interruptionCode)
		if err != nil {
			return domain.WorkAttempt{}, err
		}
		if status != domain.AttemptStatusInterrupted {
			return domain.WorkAttempt{}, activityCorruptField(nil, "interruption_reason_code", "INVALID_VALUE")
		}
	} else if status == domain.AttemptStatusInterrupted {
		return domain.WorkAttempt{}, activityCorruptField(nil, "interruption_reason_code", "REQUIRED")
	}
	var session *string
	if sessionID.Valid {
		session, err = parseActivityNullableULIDValue("session_id", sessionID.String)
		if err != nil {
			return domain.WorkAttempt{}, err
		}
	}
	var agent *string
	if agentLabel.Valid {
		agent, err = parseActivityStringPointer("agent_label", agentLabel.String)
		if err != nil {
			return domain.WorkAttempt{}, err
		}
	}
	var resultSummaryValue *string
	if resultSummary.Valid {
		value := resultSummary.String
		if err := domain.ValidateText("result_summary", value, domain.MaxAttemptNoteRunes); err != nil {
			return domain.WorkAttempt{}, activityCorrupt(err)
		}
		resultSummaryValue = &value
	}
	var reasonDetailsValue *string
	if reasonDetails.Valid {
		value := reasonDetails.String
		if err := domain.ValidateText("reason_details", value, domain.MaxAttemptNoteRunes); err != nil {
			return domain.WorkAttempt{}, activityCorrupt(err)
		}
		reasonDetailsValue = &value
	}
	var nextSteps []string
	if nextStepsJSON.Valid {
		if err := parseActivityStringArray("next_steps_json", nextStepsJSON.String, &nextSteps); err != nil {
			return domain.WorkAttempt{}, err
		}
	}
	var verification []string
	if verificationJSON.Valid {
		if err := parseActivityStringArray("verification_json", verificationJSON.String, &verification); err != nil {
			return domain.WorkAttempt{}, err
		}
	}
	return domain.WorkAttempt{ID: id, IssueID: issueID, SessionID: session, AgentLabel: agent, Kind: kind, Status: status, IssueVersionAtStart: issueVersionAtStart, ContextEventIDAtStart: contextEventIDAtStart, LeaseExpiresAt: leaseExpires, StartedAt: started, LastHeartbeatAt: lastHeartbeat, FinishedAt: finished, ResultSummary: resultSummaryValue, NextSteps: nextSteps, Verification: verification, FailureReasonCode: failureReason, InterruptionReasonCode: interruptionReason, ReasonDetails: reasonDetailsValue}, nil
}

func parseActivityFailureReason(value sql.NullString) (*domain.FailureReasonCode, error) {
	if !value.Valid {
		return nil, nil
	}
	result := domain.FailureReasonCode(value.String)
	if !result.Valid() {
		return nil, activityCorruptField(nil, "failure_reason_code", "INVALID_VALUE")
	}
	return &result, nil
}

func parseActivityInterruptionReason(value sql.NullString) (*domain.InterruptionReasonCode, error) {
	if !value.Valid {
		return nil, nil
	}
	result := domain.InterruptionReasonCode(value.String)
	if !result.Valid() {
		return nil, activityCorruptField(nil, "interruption_reason_code", "INVALID_VALUE")
	}
	return &result, nil
}

func parseActivityStringArray(field, value string, result *[]string) error {
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return activityCorruptField(err, field, "INVALID_JSON")
	}
	items, ok := parsed.([]any)
	if !ok {
		return activityCorruptField(nil, field, "INVALID_JSON")
	}
	validated := make([]string, 0, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return activityCorruptField(nil, field, "INVALID_JSON")
		}
		if err := domain.ValidateText(field+"["+strconv.Itoa(index)+"]", text, domain.MaxAttemptNoteRunes); err != nil {
			return activityCorrupt(err)
		}
		validated = append(validated, text)
	}
	*result = append([]string(nil), validated...)
	return nil
}

func parseActivityStringPointer(field, value string) (*string, error) {
	if err := domain.ValidateText(field, value, -1); err != nil {
		return nil, activityCorrupt(err)
	}
	return &value, nil
}

func parseActivityNullableULIDValue(field, value string) (*string, error) {
	if _, err := ids.ParseStrict(value); err != nil {
		return nil, activityCorruptField(err, field, "INVALID_ULID")
	}
	return &value, nil
}

func loadActivityAttemptNote(ctx context.Context, query Queryer, id string) (domain.AttemptNote, error) {
	note, err := scanActivityAttemptNote(query.QueryRowContext(ctx, `SELECT id, attempt_id, kind, content, next_steps_json, important, created_at FROM attempt_notes WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.AttemptNote{}, activityCorrupt(err)
		}
		return domain.AttemptNote{}, err
	}
	return note, nil
}

func scanActivityAttemptNote(scanner scanner) (domain.AttemptNote, error) {
	var (
		id, attemptID, kindText, content, createdAt string
		nextStepsJSON                               sql.NullString
		important                                   int64
	)
	if err := scanner.Scan(&id, &attemptID, &kindText, &content, &nextStepsJSON, &important, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return domain.AttemptNote{}, err
		}
		return domain.AttemptNote{}, activityCorrupt(err)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.AttemptNote{}, activityCorruptField(err, "id", "INVALID_ULID")
	}
	if _, err := ids.ParseStrict(attemptID); err != nil {
		return domain.AttemptNote{}, activityCorruptField(err, "attempt_id", "INVALID_ULID")
	}
	kind := domain.AttemptNoteKind(kindText)
	if !kind.Valid() {
		return domain.AttemptNote{}, activityCorruptField(nil, "kind", "INVALID_VALUE")
	}
	if err := domain.ValidateText("content", content, domain.MaxAttemptNoteRunes); err != nil {
		return domain.AttemptNote{}, activityCorrupt(err)
	}
	if strings.TrimSpace(content) == "" {
		return domain.AttemptNote{}, activityCorruptField(nil, "content", "REQUIRED")
	}
	if important != 0 && important != 1 {
		return domain.AttemptNote{}, activityCorruptField(nil, "important", "INVALID_VALUE")
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.AttemptNote{}, err
	}
	var nextSteps []string
	if nextStepsJSON.Valid {
		if err := parseActivityStringArray("next_steps_json", nextStepsJSON.String, &nextSteps); err != nil {
			return domain.AttemptNote{}, err
		}
	}
	return domain.AttemptNote{ID: id, AttemptID: attemptID, Kind: kind, Content: content, NextSteps: nextSteps, Important: important == 1, CreatedAt: created}, nil
}

func loadActivityEvent(ctx context.Context, query Queryer, entityID string) (domain.IssueEvent, error) {
	id, err := strconv.ParseInt(entityID, 10, 64)
	if err != nil || id <= 0 {
		return domain.IssueEvent{}, activityCorruptField(nil, "entity_id", "INVALID_VALUE")
	}
	event, err := scanActivityEvent(query.QueryRowContext(ctx, `SELECT id, issue_id, event_type, session_id, attempt_id, payload, created_at FROM issue_events WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.IssueEvent{}, activityCorrupt(err)
		}
		return domain.IssueEvent{}, err
	}
	return event, nil
}

func scanActivityEvent(scanner scanner) (domain.IssueEvent, error) {
	var (
		id                                     int64
		issueID, eventType, payload, createdAt sql.NullString
		sessionID, attemptID                   sql.NullString
	)
	if err := scanner.Scan(&id, &issueID, &eventType, &sessionID, &attemptID, &payload, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return domain.IssueEvent{}, err
		}
		return domain.IssueEvent{}, activityCorrupt(err)
	}
	if id <= 0 {
		return domain.IssueEvent{}, activityCorruptField(nil, "id", "INVALID_VALUE")
	}
	if !issueID.Valid || issueID.String == "" {
		return domain.IssueEvent{}, activityCorruptField(nil, "issue_id", "REQUIRED")
	}
	if _, err := ids.ParseStrict(issueID.String); err != nil {
		return domain.IssueEvent{}, activityCorruptField(err, "issue_id", "INVALID_ULID")
	}
	if !eventType.Valid || eventType.String == "" {
		return domain.IssueEvent{}, activityCorruptField(nil, "event_type", "REQUIRED")
	}
	if err := domain.ValidateText("event_type", eventType.String, -1); err != nil {
		return domain.IssueEvent{}, activityCorrupt(err)
	}
	var session *string
	if sessionID.Valid {
		if _, err := ids.ParseStrict(sessionID.String); err != nil {
			return domain.IssueEvent{}, activityCorruptField(err, "session_id", "INVALID_ULID")
		}
		session = &sessionID.String
	}
	var attempt *string
	if attemptID.Valid {
		if _, err := ids.ParseStrict(attemptID.String); err != nil {
			return domain.IssueEvent{}, activityCorruptField(err, "attempt_id", "INVALID_ULID")
		}
		attempt = &attemptID.String
	}
	if !payload.Valid {
		return domain.IssueEvent{}, activityCorruptField(nil, "payload", "REQUIRED")
	}
	compactPayload, err := compactActivityJSON("payload", payload.String)
	if err != nil {
		return domain.IssueEvent{}, err
	}
	if !createdAt.Valid {
		return domain.IssueEvent{}, activityCorruptField(nil, "created_at", "REQUIRED")
	}
	created, err := parseIssueTimestamp("created_at", createdAt.String)
	if err != nil {
		return domain.IssueEvent{}, err
	}
	return domain.IssueEvent{ID: id, IssueID: &issueID.String, EventType: eventType.String, SessionID: session, AttemptID: attempt, Payload: compactPayload, CreatedAt: created}, nil
}

func loadActivityArtifact(ctx context.Context, query Queryer, id string) (domain.Artifact, error) {
	artifact, err := scanActivityArtifact(query.QueryRowContext(ctx, `SELECT id, issue_id, attempt_id, type, uri, title, metadata, created_at FROM artifacts WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Artifact{}, activityCorrupt(err)
		}
		return domain.Artifact{}, err
	}
	return artifact, nil
}

func loadActivityReview(ctx context.Context, query Queryer, id string) (domain.ReviewRequest, error) {
	return scanActivityReview(query.QueryRowContext(ctx, `SELECT id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, status, supersedes_id, active_attempt_id, version, created_at, resolved_at FROM review_requests WHERE id = ?`, id))
}

func scanActivityReview(scanner scanner) (domain.ReviewRequest, error) {
	var request domain.ReviewRequest
	var artifactIDsJSON []byte
	var status string
	var supersedesID sql.NullString
	var activeAttemptID sql.NullString
	var createdAtText string
	var resolvedAtText sql.NullString
	var err error
	if err = scanner.Scan(&request.ID, &request.TargetID, &request.IssueID, &request.TargetIssueVersion, &request.TargetEventID, &artifactIDsJSON, &status, &supersedesID, &activeAttemptID, &request.Version, &createdAtText, &resolvedAtText); err != nil {
		if err == sql.ErrNoRows {
			return domain.ReviewRequest{}, activityCorrupt(err)
		}
		return domain.ReviewRequest{}, err
	}
	request.ArtifactIDs, err = unmarshalArtifactIDs(artifactIDsJSON)
	if err != nil {
		return domain.ReviewRequest{}, activityCorrupt(err)
	}
	request.Status = domain.ReviewRequestStatus(status)
	if supersedesID.Valid {
		value := supersedesID.String
		request.SupersedesID = &value
	}
	if activeAttemptID.Valid {
		value := activeAttemptID.String
		request.ActiveAttemptID = &value
	}
	request.CreatedAt = parseTimestamp(createdAtText)
	if resolvedAtText.Valid {
		value := parseTimestamp(resolvedAtText.String)
		request.ResolvedAt = &value
	}
	return request, nil
}

func scanActivityArtifact(scanner scanner) (domain.Artifact, error) {
	var (
		id, issueID, artifactType, uri, createdAt string
		attemptID                                 sql.NullString
		title, metadata                           sql.NullString
	)
	if err := scanner.Scan(&id, &issueID, &attemptID, &artifactType, &uri, &title, &metadata, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return domain.Artifact{}, err
		}
		return domain.Artifact{}, activityCorrupt(err)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.Artifact{}, activityCorruptField(err, "id", "INVALID_ULID")
	}
	if _, err := ids.ParseStrict(issueID); err != nil {
		return domain.Artifact{}, activityCorruptField(err, "issue_id", "INVALID_ULID")
	}
	kind := domain.ArtifactType(artifactType)
	if !kind.Valid() {
		return domain.Artifact{}, activityCorruptField(nil, "type", "INVALID_ENUM")
	}
	if err := domain.ValidateText("uri", uri, domain.MaxArtifactURIRunes); err != nil {
		return domain.Artifact{}, activityCorrupt(err)
	}
	if strings.TrimSpace(uri) == "" {
		return domain.Artifact{}, activityCorruptField(nil, "uri", "REQUIRED")
	}
	var titleValue *string
	if title.Valid {
		value := title.String
		if err := domain.ValidateText("title", value, domain.MaxTitleRunes); err != nil {
			return domain.Artifact{}, activityCorrupt(err)
		}
		if strings.TrimSpace(value) == "" {
			return domain.Artifact{}, activityCorruptField(nil, "title", "REQUIRED")
		}
		titleValue = &value
	}
	var attemptIDValue *string
	if attemptID.Valid {
		if attemptID.String == "" {
			return domain.Artifact{}, activityCorruptField(nil, "attempt_id", "REQUIRED")
		}
		if _, err := ids.ParseStrict(attemptID.String); err != nil {
			return domain.Artifact{}, activityCorruptField(err, "attempt_id", "INVALID_ULID")
		}
		attemptIDValue = &attemptID.String
	}
	var metadataValue json.RawMessage
	if metadata.Valid {
		compact, err := compactActivityJSON("metadata", metadata.String)
		if err != nil {
			return domain.Artifact{}, err
		}
		metadataValue = compact
	}
	input := domain.ArtifactInput{Type: kind, URI: uri, Title: titleValue, Metadata: metadataValue}
	validated, err := domain.ValidateArtifactInputs("artifact", []domain.ArtifactInput{input})
	if err != nil {
		return domain.Artifact{}, activityCorrupt(err)
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.Artifact{}, err
	}
	artifactInput := validated[0]
	return domain.Artifact{ID: id, IssueID: issueID, AttemptID: attemptIDValue, Type: artifactInput.Type, URI: artifactInput.URI, Title: artifactInput.Title, Metadata: artifactInput.Metadata, CreatedAt: created}, nil
}

// loadActivityReservation reads only the columns domain.SummarizeReservation
// needs: comparison_value and normalized_json are intentionally excluded so
// they never reach activity payloads (ISSUE-181/ISSUE-182). This is a
// deliberately narrower scan than reservations.go's scanReservationRow,
// which loads the full row including those internal-only columns.
func loadActivityReservation(ctx context.Context, query Queryer, id string) (domain.Reservation, error) {
	reservation, err := scanActivityReservation(query.QueryRowContext(ctx,
		`SELECT id, issue_id, attempt_id, kind, display_value, status, created_at, released_at, release_reason FROM resource_reservations WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Reservation{}, activityCorrupt(err)
		}
		return domain.Reservation{}, err
	}
	return reservation, nil
}

func scanActivityReservation(scanner scanner) (domain.Reservation, error) {
	var (
		id, issueID, attemptID, kindText, displayValue, statusText, createdAt string
		releasedAt, releaseReasonText                                         sql.NullString
	)
	if err := scanner.Scan(&id, &issueID, &attemptID, &kindText, &displayValue, &statusText, &createdAt, &releasedAt, &releaseReasonText); err != nil {
		if err == sql.ErrNoRows {
			return domain.Reservation{}, err
		}
		return domain.Reservation{}, activityCorrupt(err)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.Reservation{}, activityCorruptField(err, "id", "INVALID_ULID")
	}
	if _, err := ids.ParseStrict(issueID); err != nil {
		return domain.Reservation{}, activityCorruptField(err, "issue_id", "INVALID_ULID")
	}
	if _, err := ids.ParseStrict(attemptID); err != nil {
		return domain.Reservation{}, activityCorruptField(err, "attempt_id", "INVALID_ULID")
	}
	kind := domain.ResourceKind(kindText)
	if !kind.Valid() {
		return domain.Reservation{}, activityCorruptField(nil, "kind", "INVALID_ENUM")
	}
	if strings.TrimSpace(displayValue) == "" {
		return domain.Reservation{}, activityCorruptField(nil, "display_value", "REQUIRED")
	}
	status := domain.ReservationStatus(statusText)
	if status != domain.ReservationStatusActive && status != domain.ReservationStatusReleased {
		return domain.Reservation{}, activityCorruptField(nil, "status", "INVALID_ENUM")
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.Reservation{}, err
	}
	released, err := parseNullableIssueTimestamp("released_at", releasedAt)
	if err != nil {
		return domain.Reservation{}, err
	}
	var releaseReason *domain.ReservationReleaseReason
	if releaseReasonText.Valid {
		reason := domain.ReservationReleaseReason(releaseReasonText.String)
		if !reason.Valid() {
			return domain.Reservation{}, activityCorruptField(nil, "release_reason", "INVALID_ENUM")
		}
		releaseReason = &reason
	}
	if (status == domain.ReservationStatusActive) != (released == nil) {
		return domain.Reservation{}, activityCorruptField(nil, "released_at", "STATUS_MISMATCH")
	}
	if (status == domain.ReservationStatusActive) != (releaseReason == nil) {
		return domain.Reservation{}, activityCorruptField(nil, "release_reason", "STATUS_MISMATCH")
	}
	return domain.Reservation{
		ID: id, IssueID: issueID, AttemptID: attemptID, Kind: kind, DisplayValue: displayValue,
		Status: status, CreatedAt: created, ReleasedAt: released, ReleaseReason: releaseReason,
	}, nil
}

func parseActivityNullableULID(field string, value sql.NullString) (*string, error) {
	if !value.Valid {
		return nil, nil
	}
	if _, err := ids.ParseStrict(value.String); err != nil {
		return nil, activityCorruptField(err, field, "INVALID_ULID")
	}
	result := value.String
	return &result, nil
}

func compactActivityJSON(field, value string) (json.RawMessage, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(value)); err != nil {
		return nil, activityCorruptField(err, field, "INVALID_JSON")
	}
	return append(json.RawMessage(nil), compact.Bytes()...), nil
}

func validateActivityInput(input domain.GetIssueActivityInput) (domain.GetIssueActivityInput, error) {
	if input.Cursor != "" && utf8.RuneCountInString(input.Cursor) > 4096 {
		return domain.GetIssueActivityInput{}, activityCursorError(pagination.ErrCursorTooLarge)
	}
	return input.Validate()
}

func activityCursorError(err error) error {
	code := "MALFORMED_CURSOR"
	if errors.Is(err, pagination.ErrCursorTooLarge) {
		code = "CURSOR_TOO_LARGE"
	} else if errors.Is(err, pagination.ErrUnsupportedVersion) {
		code = "UNSUPPORTED_CURSOR_VERSION"
	}
	return domain.NewError(domain.CodeInvalidArgument, "activity cursor is invalid", false,
		domain.Detail{Field: "cursor", Code: code})
}

func activityCorrupt(cause error) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored activity projection is invalid", false)
}

func activityCorruptField(cause error, field, code string) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored activity projection is invalid", false,
		domain.Detail{Field: field, Code: code})
}

type scanner interface {
	Scan(dest ...any) error
}
