package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

const testSessionID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

type recordingSessionRepository struct {
	create ports.CreateAgentSessionCommand
	touch  ports.TouchAgentSessionCommand
	end    ports.EndAgentSessionCommand
	calls  int
}

func (r *recordingSessionRepository) CreateAgentSession(_ context.Context, command ports.CreateAgentSessionCommand) (domain.AgentSession, error) {
	r.create = command
	r.calls++
	return command.Session, nil
}
func (r *recordingSessionRepository) TouchAgentSession(_ context.Context, command ports.TouchAgentSessionCommand) (domain.AgentSession, error) {
	r.touch = command
	r.calls++
	return domain.AgentSession{ID: command.SessionID}, nil
}
func (r *recordingSessionRepository) EndAgentSession(_ context.Context, command ports.EndAgentSessionCommand) (domain.AgentSession, error) {
	r.end = command
	r.calls++
	return domain.AgentSession{ID: command.SessionID}, nil
}

type countingClock struct {
	now   time.Time
	calls int
}

func (c *countingClock) Now() time.Time {
	c.calls++
	return c.now
}

type sessionIDGenerator struct {
	id  string
	err error
}

func (g sessionIDGenerator) New() (string, error) { return g.id, g.err }

func TestAgentSessionServiceCreateNormalizesAndUsesUTCClockOnce(t *testing.T) {
	source := &countingClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))}
	repository := &recordingSessionRepository{}
	service, err := application.NewAgentSessionService(repository, source, sessionIDGenerator{id: testSessionID})
	if err != nil {
		t.Fatal(err)
	}
	version := " 1 "
	result, err := service.Create(context.Background(), domain.CreateAgentSessionInput{
		ClientName: " client ", ClientVersion: &version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || repository.calls != 1 || result.ID != testSessionID ||
		result.ClientName != "client" || result.ClientVersion == nil || *result.ClientVersion != "1" ||
		repository.create.Session.StartedAt.Location() != time.UTC ||
		!repository.create.Session.StartedAt.Equal(source.now.UTC()) ||
		!repository.create.Session.LastSeenAt.Equal(source.now.UTC()) {
		t.Fatalf("clock calls = %d, command = %#v, result = %#v", source.calls, repository.create, result)
	}
	version = "changed"
	if *repository.create.Session.ClientVersion != "1" {
		t.Fatal("create command aliases input metadata")
	}
}

func TestAgentSessionServiceValidatesDependenciesAndGeneratedID(t *testing.T) {
	source := clock.NewFakeClock(time.Now())
	repository := &recordingSessionRepository{}
	if _, err := application.NewAgentSessionService(nil, source, sessionIDGenerator{id: testSessionID}); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("nil repository error = %v", err)
	}
	if _, err := application.NewAgentSessionService(repository, nil, sessionIDGenerator{id: testSessionID}); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("nil clock error = %v", err)
	}
	if _, err := application.NewAgentSessionService(repository, source, nil); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("nil generator error = %v", err)
	}
	service, err := application.NewAgentSessionService(repository, source, sessionIDGenerator{id: "bad"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), domain.CreateAgentSessionInput{ClientName: "client"})
	if !errors.Is(err, &domain.Error{Code: domain.CodeIDGeneration}) || repository.calls != 0 {
		t.Fatalf("invalid generated ID error = %v, calls = %d", err, repository.calls)
	}
	service, err = application.NewAgentSessionService(repository, source, sessionIDGenerator{err: errors.New("entropy failed")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), domain.CreateAgentSessionInput{ClientName: "client"})
	if !errors.Is(err, &domain.Error{Code: domain.CodeIDGeneration}) || repository.calls != 0 {
		t.Fatalf("generator error = %v, calls = %d", err, repository.calls)
	}
}

func TestAgentSessionServiceTouchEndValidateIDBeforeClockOrRepository(t *testing.T) {
	source := &countingClock{now: time.Now()}
	repository := &recordingSessionRepository{}
	service, err := application.NewAgentSessionService(repository, source, sessionIDGenerator{id: testSessionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Touch(context.Background(), "bad"); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("touch error = %v", err)
	}
	if _, err := service.End(context.Background(), "bad"); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("end error = %v", err)
	}
	if source.calls != 0 || repository.calls != 0 {
		t.Fatalf("invalid lifecycle calls: clock %d repository %d", source.calls, repository.calls)
	}
	if _, err := service.Touch(context.Background(), testSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.End(context.Background(), testSessionID); err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 || !repository.touch.OccurredAt.Equal(source.now.UTC()) ||
		!repository.end.OccurredAt.Equal(source.now.UTC()) {
		t.Fatalf("clock calls = %d, touch = %#v, end = %#v", source.calls, repository.touch, repository.end)
	}
}
