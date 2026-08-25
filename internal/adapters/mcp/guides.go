package mcp

import (
	"context"
	"embed"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const initializeInstructions = "Start with open_project using the absolute project root. Retain its project_ref and pass it to every subsequent project-scoped tool call; routing is stateless. Then use get_planning_graph or list_issues to find claimable work. Read get_work_context before claim_issue. While working, renew the lease and save restartable checkpoints; always finish_attempt on completion, failure, or handoff. Use expected_version for issue writes. Detailed guides: rhizome://guides/agent-workflow."

//go:generate go run ./guidesync

//go:embed guide_assets/*.md
var guideAssetsFS embed.FS

type guide struct {
	URI         string
	Name        string
	Title       string
	Description string
	File        string
	Content     string
}

var guides = []guide{
	{
		URI:         "rhizome://guides/agent-workflow",
		Name:        "agent-workflow",
		Title:       "Rhizome Agent Workflow",
		Description: "End-to-end workflow for selecting, claiming, executing, and finishing tracked work.",
		File:        "agent-workflow.md",
		Content:     mustReadGuideAsset("guide_assets/agent-workflow.md"),
	},
	{
		URI:         "rhizome://guides/issue-lifecycle",
		Name:        "issue-lifecycle",
		Title:       "Rhizome Issue Lifecycle",
		Description: "Status, dependency, review, versioning, and archival rules for issues.",
		File:        "issue-lifecycle.md",
		Content:     mustReadGuideAsset("guide_assets/issue-lifecycle.md"),
	},
	{
		URI:         "rhizome://guides/multi-agent-handoff",
		Name:        "multi-agent-handoff",
		Title:       "Rhizome Multi-Agent Handoff",
		Description: "Durable checkpoint, interruption, recovery, and review guidance across agents.",
		File:        "multi-agent-handoff.md",
		Content:     mustReadGuideAsset("guide_assets/multi-agent-handoff.md"),
	},
}

func mustReadGuideAsset(path string) string {
	contents, err := guideAssetsFS.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return string(contents)
}

func registerGuides(server *sdkmcp.Server) {
	for _, item := range guides {
		item := item
		server.AddResource(&sdkmcp.Resource{
			URI: item.URI, Name: item.Name, Title: item.Title,
			Description: item.Description, MIMEType: "text/markdown", Size: int64(len(item.Content)),
		}, func(context.Context, *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
			return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{
				URI: item.URI, MIMEType: "text/markdown", Text: item.Content,
			}}}, nil
		})
	}
}

func guideLinks() []guideLinkDTO {
	result := make([]guideLinkDTO, len(guides))
	for index, item := range guides {
		result[index] = guideLinkDTO{URI: item.URI, Title: item.Title, Description: item.Description}
	}
	return result
}
