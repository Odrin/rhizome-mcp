package mcp

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestSchemaConstructorsProduceMarshalableSchemas(t *testing.T) {
	tests := map[string]func() *jsonschema.Schema{
		"get project":                   schemaGetProject,
		"open project":                  schemaOpenProject,
		"create agent session":          schemaCreateAgentSession,
		"end agent session":             schemaEndAgentSession,
		"export project":                schemaExportProject,
		"export project output":         schemaExportProjectOutput,
		"validate import":               schemaValidateImport,
		"validate import output":        schemaValidateImportOutput,
		"apply import":                  schemaApplyImport,
		"apply import output":           schemaApplyImportOutput,
		"list labels":                   schemaListLabels,
		"create issue":                  schemaCreateIssue,
		"update issue":                  schemaUpdateIssue,
		"get issue":                     schemaGetIssue,
		"get issue activity":            schemaGetIssueActivity,
		"search":                        schemaSearch,
		"get changes":                   schemaGetChanges,
		"get work context":              schemaGetWorkContext,
		"list issues":                   schemaListIssues,
		"archive issue":                 schemaArchiveIssue,
		"create review request":         schemaCreateReviewRequest,
		"get review request":            schemaGetReviewRequest,
		"list review requests":          schemaListReviewRequests,
		"cancel review request":         schemaCancelReviewRequest,
		"supersede review request":      schemaSupersedeReviewRequest,
		"replace review request":        schemaReplaceReviewRequest,
		"add comment":                   schemaAddComment,
		"record decision":               schemaRecordDecision,
		"list decisions":                schemaListDecisions,
		"manage issue relation":         schemaManageIssueRelation,
		"get issue graph":               schemaGetIssueGraph,
		"get planning graph":            schemaGetPlanningGraph,
		"validate issue plan":           schemaValidateIssuePlan,
		"apply issue plan":              schemaApplyIssuePlan,
		"claim issue":                   schemaClaimIssue,
		"renew attempt":                 schemaRenewAttempt,
		"save attempt note":             schemaSaveAttemptNote,
		"finish attempt":                schemaFinishAttempt,
		"project output":                schemaProjectOutput,
		"create agent session output":   schemaCreateAgentSessionOutput,
		"end agent session output":      schemaEndAgentSessionOutput,
		"label list output":             schemaLabelListOutput,
		"issue output":                  schemaIssueOutput,
		"create issue output":           schemaCreateIssueOutput,
		"archive issue output":          schemaArchiveIssueOutput,
		"get issue output":              schemaGetIssueOutput,
		"review request output":         schemaReviewRequestOutput,
		"review request list output":    schemaReviewRequestListOutput,
		"replace review request output": schemaReplaceReviewRequestOutput,
		"issue activity output":         schemaGetIssueActivityOutput,
		"search output":                 schemaSearchOutput,
		"changes output":                schemaChangesOutput,
		"comment output":                schemaAddCommentOutput,
		"decision output":               schemaRecordDecisionOutput,
		"decision list output":          schemaDecisionListOutput,
		"work context output":           schemaGetWorkContextOutput,
		"update output":                 schemaUpdateOutput,
		"issue list output":             schemaIssueListOutput,
		"issue relation output":         schemaManageIssueRelationOutput,
		"graph output":                  schemaGraphOutput,
		"plan validation output":        schemaPlanValidationOutput,
		"apply issue plan output":       schemaApplyIssuePlanOutput,
		"claim issue output":            schemaClaimIssueOutput,
		"renew attempt output":          schemaRenewAttemptOutput,
		"save attempt note output":      schemaSaveAttemptNoteOutput,
		"finish attempt output":         schemaFinishAttemptOutput,
	}

	for name, constructor := range tests {
		t.Run(name, func(t *testing.T) {
			schema := constructor()
			if schema == nil {
				t.Fatal("constructor returned nil")
			}
			if _, err := json.Marshal(schema); err != nil {
				t.Fatalf("marshal schema: %v", err)
			}
		})
	}
}

func TestOutputSchemaConstructorsProduceTopLevelObjects(t *testing.T) {
	constructors := map[string]func() *jsonschema.Schema{
		"export project": schemaExportProjectOutput,
		"create issue":   schemaCreateIssueOutput,
		"update issue":   schemaUpdateOutput,
		"archive issue":  schemaArchiveIssueOutput,
		"claim issue":    schemaClaimIssueOutput,
		"finish attempt": schemaFinishAttemptOutput,
	}

	for name, constructor := range constructors {
		t.Run(name, func(t *testing.T) {
			if schema := constructor(); schema.Type != "object" {
				t.Fatalf("output schema type = %q, want object", schema.Type)
			}
		})
	}
}
