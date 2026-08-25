package runtime_test

import (
	"context"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	projectruntime "rhizome-mcp/internal/runtime"
)

func TestProjectDoctorHealthyNormalAndFull(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	fakeClock := clock.NewFakeClock(testTime)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	report, err := project.Doctor(context.Background(), projectruntime.DoctorOptions{Full: false})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if !report.Healthy() {
		t.Fatalf("Doctor() unhealthy report = %+v", report)
	}
	wantChecks := []string{"ping", "journal_mode_wal", "foreign_keys_enabled", "schema_version", "migration_history", "fts5", "quick_check", "foreign_key_check", "one_active_attempt_per_issue", "database_writable", "data_directory_writable", "free_disk_space", "wal_size", "expired_active_attempts", "active_reservations_without_live_attempt", "reservation_release_state_consistency", "http_address", "workflow_policy_payload_shape", "review_targets_without_gate_snapshot", "gate_evidence_attempt_issue_consistency"}
	if len(report.Checks) != len(wantChecks) {
		t.Fatalf("doctor checks = %+v", report.Checks)
	}
	for index, want := range wantChecks {
		if report.Checks[index].Name != want {
			t.Fatalf("doctor check %d = %q, want %q", index, report.Checks[index].Name, want)
		}
	}

	fullReport, err := project.Doctor(context.Background(), projectruntime.DoctorOptions{Full: true})
	if err != nil {
		t.Fatalf("Doctor(full) error = %v", err)
	}
	if !fullReport.Healthy() {
		t.Fatalf("Doctor(full) unhealthy report = %+v", fullReport)
	}
	if fullReport.Checks[len(fullReport.Checks)-1].Name != "integrity_check" {
		t.Fatalf("full doctor last check = %q", fullReport.Checks[len(fullReport.Checks)-1].Name)
	}
}

func TestProjectDoctorReportsInvalidHTTPAddressConfiguration(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	fakeClock := clock.NewFakeClock(testTime)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	report, err := project.Doctor(context.Background(), projectruntime.DoctorOptions{Full: false, HTTPAddress: "localhost:0"})
	if err == nil {
		t.Fatalf("Doctor() unexpectedly succeeded: %+v", report)
	}
	assertDomainCode(t, err, projectruntime.CodeHealthCheck)
	if report.Healthy() {
		t.Fatalf("Doctor() healthy report = %+v", report)
	}
	var found bool
	for _, check := range report.Checks {
		if check.Name == "http_address" {
			found = true
			if check.Healthy {
				t.Fatalf("http_address unexpectedly healthy: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("doctor report missing http_address check: %+v", report.Checks)
	}
}

func TestProjectDoctorReportsExpiredActiveAttemptsWithoutMutatingState(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	fakeClock := clock.NewFakeClock(testTime)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	issueID := "01F2H8V5M9Q1J7K3N6P4R0T2WX"
	attemptID := "01G2H8V5M9Q1J7K3N6P4R0T2WX"
	leaseExpiresAt := fakeClock.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	now := fakeClock.Now().UTC().Format(time.RFC3339Nano)
	if err := project.Database.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(
			id, sequence_no, type, title, status, priority, version, created_at, updated_at
		) VALUES (?, 1, 'task', 'doctor state', 'ready', 'medium', 1, ?, ?)`, issueID, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'01', ?, ?, ?)`, attemptID, issueID, leaseExpiresAt, now, now)
		return err
	}); err != nil {
		t.Fatalf("insert test attempt: %v", err)
	}

	report, err := project.Doctor(context.Background(), projectruntime.DoctorOptions{Full: false})
	if err == nil {
		t.Fatalf("Doctor() unexpectedly succeeded: %+v", report)
	}
	assertDomainCode(t, err, projectruntime.CodeHealthCheck)
	if report.Healthy() {
		t.Fatalf("Doctor() healthy report = %+v", report)
	}
	var status string
	var eventCount int
	if err := project.Database.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT status FROM work_attempts WHERE id = ?", attemptID).Scan(&status); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&eventCount)
	}); err != nil {
		t.Fatalf("read attempt state: %v", err)
	}
	if status != "active" {
		t.Fatalf("attempt status = %q, want active", status)
	}
	if eventCount != 0 {
		t.Fatalf("issue event rows = %d, want 0", eventCount)
	}
	var found bool
	for _, check := range report.Checks {
		if check.Name == "expired_active_attempts" {
			found = true
			if check.Healthy {
				t.Fatalf("expired_active_attempts unexpectedly healthy: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("doctor report missing expired_active_attempts check: %+v", report.Checks)
	}
}

func TestProjectDoctorHealthyWithActiveReservation(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	fakeClock := clock.NewFakeClock(testTime)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	issueID := "01F2H8V5M9Q1J7K3N6P4R0T3WX"
	attemptID := "01G2H8V5M9Q1J7K3N6P4R0T3WX"
	reservationID := "01H2H8V5M9Q1J7K3N6P4R0T3WX"
	leaseExpiresAt := fakeClock.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	now := fakeClock.Now().UTC().Format(time.RFC3339Nano)
	if err := project.Database.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(
			id, sequence_no, type, title, status, priority, version, created_at, updated_at
		) VALUES (?, 1, 'task', 'doctor state', 'ready', 'medium', 1, ?, ?)`, issueID, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'01', ?, ?, ?)`, attemptID, issueID, leaseExpiresAt, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO resource_reservations(
			id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json,
			status, version, created_at, released_at, release_reason
		) VALUES (?, ?, ?, 'file', 'main.go', 'main.go', '{}', 'active', 1, ?, NULL, NULL)`,
			reservationID, issueID, attemptID, now)
		return err
	}); err != nil {
		t.Fatalf("insert test fixtures: %v", err)
	}

	for _, full := range []bool{false, true} {
		report, err := project.Doctor(context.Background(), projectruntime.DoctorOptions{Full: full})
		if err != nil {
			t.Fatalf("Doctor(Full=%v) error = %v", full, err)
		}
		if !report.Healthy() {
			t.Fatalf("Doctor(Full=%v) unhealthy report = %+v", full, report)
		}
		wantHealthyChecks := []string{"active_reservations_without_live_attempt", "reservation_release_state_consistency"}
		for _, name := range wantHealthyChecks {
			var found bool
			for _, check := range report.Checks {
				if check.Name == name {
					found = true
					if !check.Healthy {
						t.Fatalf("%s unexpectedly unhealthy: %+v", name, check)
					}
				}
			}
			if !found {
				t.Fatalf("doctor report missing %s check: %+v", name, report.Checks)
			}
		}
	}
}

func TestProjectDoctorReportsOrphanedActiveReservationWithoutMutatingState(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	fakeClock := clock.NewFakeClock(testTime)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	issueID := "01F2H8V5M9Q1J7K3N6P4R0T4WX"
	attemptID := "01G2H8V5M9Q1J7K3N6P4R0T4WX"
	reservationID := "01H2H8V5M9Q1J7K3N6P4R0T4WX"
	leaseExpiresAt := fakeClock.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	now := fakeClock.Now().UTC().Format(time.RFC3339Nano)
	if err := project.Database.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(
			id, sequence_no, type, title, status, priority, version, created_at, updated_at
		) VALUES (?, 1, 'task', 'doctor state', 'ready', 'medium', 1, ?, ?)`, issueID, now, now); err != nil {
			return err
		}
		// The attempt has already completed, but its reservation was never released --
		// a half-restored or corrupted database state doctor must catch.
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, finished_at
		) VALUES (?, ?, 'work', 'completed', 1, 0, X'01', ?, ?, ?, ?)`, attemptID, issueID, leaseExpiresAt, now, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO resource_reservations(
			id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json,
			status, version, created_at, released_at, release_reason
		) VALUES (?, ?, ?, 'file', 'main.go', 'main.go', '{}', 'active', 1, ?, NULL, NULL)`,
			reservationID, issueID, attemptID, now)
		return err
	}); err != nil {
		t.Fatalf("insert test fixtures: %v", err)
	}

	report, err := project.Doctor(context.Background(), projectruntime.DoctorOptions{Full: false})
	if err == nil {
		t.Fatalf("Doctor() unexpectedly succeeded: %+v", report)
	}
	assertDomainCode(t, err, projectruntime.CodeHealthCheck)
	if report.Healthy() {
		t.Fatalf("Doctor() healthy report = %+v", report)
	}

	var reservationStatus string
	var eventCount int
	if err := project.Database.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT status FROM resource_reservations WHERE id = ?", reservationID).Scan(&reservationStatus); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&eventCount)
	}); err != nil {
		t.Fatalf("read reservation state: %v", err)
	}
	if reservationStatus != "active" {
		t.Fatalf("reservation status = %q, want active", reservationStatus)
	}
	if eventCount != 0 {
		t.Fatalf("issue event rows = %d, want 0", eventCount)
	}

	var found bool
	for _, check := range report.Checks {
		if check.Name == "active_reservations_without_live_attempt" {
			found = true
			if check.Healthy {
				t.Fatalf("active_reservations_without_live_attempt unexpectedly healthy: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("doctor report missing active_reservations_without_live_attempt check: %+v", report.Checks)
	}
}

func TestProjectDoctorReportsReservationReleaseStateInconsistency(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	fakeClock := clock.NewFakeClock(testTime)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	issueID := "01F2H8V5M9Q1J7K3N6P4R0T5WX"
	attemptID := "01G2H8V5M9Q1J7K3N6P4R0T5WX"
	reservationID := "01H2H8V5M9Q1J7K3N6P4R0T5WX"
	leaseExpiresAt := fakeClock.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	now := fakeClock.Now().UTC().Format(time.RFC3339Nano)
	if err := project.Database.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(
			id, sequence_no, type, title, status, priority, version, created_at, updated_at
		) VALUES (?, 1, 'task', 'doctor state', 'ready', 'medium', 1, ?, ?)`, issueID, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'01', ?, ?, ?)`, attemptID, issueID, leaseExpiresAt, now, now); err != nil {
			return err
		}
		// A released reservation missing its release_reason -- the release-state
		// invariant requires both released_at and release_reason together. The
		// schema's own CHECK constraint would normally reject this row, so it is
		// disabled for this one insert to simulate the corrupted/half-restored
		// state doctor exists to detect.
		if _, err := tx.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_reservations(
			id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json,
			status, version, created_at, released_at, release_reason
		) VALUES (?, ?, ?, 'file', 'main.go', 'main.go', '{}', 'released', 2, ?, ?, NULL)`,
			reservationID, issueID, attemptID, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `PRAGMA ignore_check_constraints = OFF`)
		return err
	}); err != nil {
		t.Fatalf("insert test fixtures: %v", err)
	}

	report, err := project.Doctor(context.Background(), projectruntime.DoctorOptions{Full: false})
	if err == nil {
		t.Fatalf("Doctor() unexpectedly succeeded: %+v", report)
	}
	assertDomainCode(t, err, projectruntime.CodeHealthCheck)
	if report.Healthy() {
		t.Fatalf("Doctor() healthy report = %+v", report)
	}

	var found bool
	for _, check := range report.Checks {
		if check.Name == "reservation_release_state_consistency" {
			found = true
			if check.Healthy {
				t.Fatalf("reservation_release_state_consistency unexpectedly healthy: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("doctor report missing reservation_release_state_consistency check: %+v", report.Checks)
	}
}

// TestProjectDoctorReportsGateInvariantViolations covers the three
// workflow-gate invariants (ISSUE-175 AC4): a policy blob with the wrong
// JSON shape, a review target without its frozen gate snapshot, and an
// evidence row naming a different issue than its owning attempt. Each is a
// state no sanctioned write path produces, seeded directly to simulate the
// corrupted or half-restored databases doctor exists to detect.
func TestProjectDoctorReportsGateInvariantViolations(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	fakeClock := clock.NewFakeClock(testTime)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	issueID := "01F2H8V5M9Q1J7K3N6P4R0T5G1"
	otherIssueID := "01F2H8V5M9Q1J7K3N6P4R0T5G2"
	attemptID := "01G2H8V5M9Q1J7K3N6P4R0T5G3"
	policyID := "01G2H8V5M9Q1J7K3N6P4R0T5G4"
	targetID := "01G2H8V5M9Q1J7K3N6P4R0T5G5"
	evidenceID := "01G2H8V5M9Q1J7K3N6P4R0T5G6"
	now := fakeClock.Now().UTC().Format(time.RFC3339Nano)
	if err := project.Database.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'gate doctor issue', 'ready', 'medium', 1, ?, ?)`, []any{issueID, now, now}},
			{`INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 2, 'task', 'other issue', 'ready', 'medium', 1, ?, ?)`, []any{otherIssueID, now, now}},
			{`INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary) VALUES (?, ?, 'work', 'completed', 1, 0, X'01', ?, ?, ?, ?, 'done')`, []any{attemptID, issueID, now, now, now, now}},
			// A policy whose requirements blob is valid JSON of the wrong
			// shape -- json_valid passes, json_type does not.
			{`INSERT INTO workflow_policies(id, selector_json, requirements_json, status, version, created_at, updated_at) VALUES (?, '{"issue_types":["task"]}', '"not-an-array"', 'active', 1, ?, ?)`, []any{policyID, now, now}},
			// A review target with no snapshot row at all.
			{`INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at) VALUES (?, ?, 1, 0, '[]', 1, ?)`, []any{targetID, issueID, now}},
			// Evidence naming a different issue than its attempt's.
			{`INSERT INTO gate_evidence(id, attempt_id, issue_id, key, result, summary, artifact_ids_json, version, created_at, updated_at) VALUES (?, ?, ?, 'impl', 'satisfied', 'divergent', '[]', 1, ?, ?)`, []any{evidenceID, attemptID, otherIssueID, now, now}},
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("insert test fixtures: %v", err)
	}

	report, err := project.Doctor(context.Background(), projectruntime.DoctorOptions{Full: false})
	if err == nil {
		t.Fatalf("Doctor() unexpectedly succeeded: %+v", report)
	}
	assertDomainCode(t, err, projectruntime.CodeHealthCheck)
	if report.Healthy() {
		t.Fatalf("Doctor() healthy report = %+v", report)
	}

	unhealthy := map[string]bool{}
	for _, check := range report.Checks {
		if !check.Healthy {
			unhealthy[check.Name] = true
		}
	}
	for _, want := range []string{
		"workflow_policy_payload_shape",
		"review_targets_without_gate_snapshot",
		"gate_evidence_attempt_issue_consistency",
	} {
		if !unhealthy[want] {
			t.Fatalf("doctor report missing unhealthy %s check: %+v", want, report.Checks)
		}
	}
}
