package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCreateAgentSessionInputValidateNormalizesAndCopiesMetadata(t *testing.T) {
	clientVersion := "  1.2  "
	label := " Luna "
	input := CreateAgentSessionInput{
		ClientName: "  client  ", ClientVersion: &clientVersion, AgentLabel: &label,
	}
	normalized, err := input.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ClientName != "client" || *normalized.ClientVersion != "1.2" || *normalized.AgentLabel != "Luna" {
		t.Fatalf("normalized input = %#v", normalized)
	}
	clientVersion = "changed"
	label = "changed"
	if *normalized.ClientVersion != "1.2" || *normalized.AgentLabel != "Luna" {
		t.Fatal("normalized pointers alias input")
	}
	if normalized.Model != nil || normalized.InstanceKey != nil {
		t.Fatal("omitted optional values must remain nil")
	}
}

func TestCreateAgentSessionInputValidateRejectsRequiredAndOptionalBlanks(t *testing.T) {
	if _, err := (CreateAgentSessionInput{ClientName: " \t"}).Validate(); !errors.Is(err, &Error{Code: CodeInvalidArgument}) {
		t.Fatalf("blank client name error = %v", err)
	}
	blank := " \n "
	_, err := (CreateAgentSessionInput{ClientName: "client", Model: &blank}).Validate()
	if !errors.Is(err, &Error{Code: CodeInvalidArgument}) {
		t.Fatalf("blank optional error = %v", err)
	}
	var domainErr *Error
	if !errors.As(err, &domainErr) || len(domainErr.Details) != 1 || domainErr.Details[0].Field != "model" {
		t.Fatalf("blank optional details = %#v", err)
	}
}

func TestCreateAgentSessionInputValidateRejectsMetadataOverLimit(t *testing.T) {
	tooLong := strings.Repeat("x", MaxSessionMetadataRunes+1)
	_, err := (CreateAgentSessionInput{ClientName: "client", InstanceKey: &tooLong}).Validate()
	if !errors.Is(err, &Error{Code: CodeLimitExceeded}) {
		t.Fatalf("long metadata error = %v", err)
	}
}

func TestAgentSessionCloneCopiesPointers(t *testing.T) {
	version, ended := "1", testSessionTime()
	session := AgentSession{
		ID: "id", ClientName: "client", ClientVersion: &version, EndedAt: &ended,
	}
	clone := session.Clone()
	*clone.ClientVersion = "2"
	clone.EndedAt = nil
	if *session.ClientVersion != "1" || session.EndedAt == nil {
		t.Fatal("clone aliases session pointers")
	}
}

func testSessionTime() (resultTime time.Time) {
	return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
}
