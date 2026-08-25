package compose

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/projectconfig"
	"rhizome-mcp/internal/runtime"
)

// Services is the project-local service bundle shared by every adapter.
type Services struct {
	project *runtime.Project

	bundle application.Bundle

	closeOnce sync.Once
	closeErr  error
}

// ProjectRef returns the project's canonical reference.
func (s *Services) ProjectRef() string {
	if s == nil || s.project == nil {
		return ""
	}
	return s.project.ProjectID
}

// Bundle returns the service bundle.
func (s *Services) Bundle() application.Bundle {
	if s == nil {
		return application.Bundle{}
	}
	return s.bundle
}

// Project returns the underlying project.
func (s *Services) Project() *runtime.Project {
	if s == nil {
		return nil
	}
	return s.project
}

// Close closes the project and its associated resources.
func (s *Services) Close(ctx context.Context) error {
	if s == nil || s.project == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.closeErr = s.project.Close(closeCtx)
	})
	return s.closeErr
}

// NewServices composes all services for a project.
func NewServices(project *runtime.Project, source clock.Clock) (*Services, error) {
	if project == nil {
		return nil, errors.New("project is nil")
	}

	if source == nil {
		source = clock.RealClock{}
	}
	issueRepository, err := sqlite.NewIssueRepository(project.Database)
	if err != nil {
		return nil, err
	}
	projectRepository, err := sqlite.NewProjectRepository(project.Database)
	if err != nil {
		return nil, err
	}
	relationRepository, err := sqlite.NewRelationRepository(project.Database)
	if err != nil {
		return nil, err
	}
	graphRepository, err := sqlite.NewGraphRepository(project.Database)
	if err != nil {
		return nil, err
	}
	planningRepository, err := sqlite.NewPlanningRepository(project.Database)
	if err != nil {
		return nil, err
	}
	commentRepository, err := sqlite.NewCommentRepository(project.Database)
	if err != nil {
		return nil, err
	}
	decisionRepository, err := sqlite.NewDecisionRepository(project.Database)
	if err != nil {
		return nil, err
	}
	activityRepository, err := sqlite.NewActivityRepository(project.Database)
	if err != nil {
		return nil, err
	}
	searchRepository, err := sqlite.NewSearchRepository(project.Database)
	if err != nil {
		return nil, err
	}
	reviewRepository, err := sqlite.NewReviewRepository(project.Database)
	if err != nil {
		return nil, err
	}
	searchIndexRepository, err := sqlite.NewSearchIndexRepository(project.Database)
	if err != nil {
		return nil, err
	}
	attemptRepository, err := sqlite.NewAttemptRepository(project.Database)
	if err != nil {
		return nil, err
	}
	reservationRepository, err := sqlite.NewReservationRepository(project.Database)
	if err != nil {
		return nil, err
	}
	workContextRepository, err := sqlite.NewWorkContextRepository(project.Database)
	if err != nil {
		return nil, err
	}
	workflowPolicyRepository, err := sqlite.NewWorkflowPolicyRepository(project.Database)
	if err != nil {
		return nil, err
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		return nil, err
	}
	issueService, err := application.NewIssueService(issueRepository, source, generator)
	if err != nil {
		return nil, err
	}
	projectService, err := application.NewProjectService(projectRepository, generator)
	if err != nil {
		return nil, err
	}
	relationService, err := application.NewRelationService(relationRepository, source, generator)
	if err != nil {
		return nil, err
	}
	graphService, err := application.NewGraphService(graphRepository, source)
	if err != nil {
		return nil, err
	}
	planningService, err := application.NewPlanningService(planningRepository, source, generator)
	if err != nil {
		return nil, err
	}
	commentService, err := application.NewCommentService(commentRepository, source, generator)
	if err != nil {
		return nil, err
	}
	decisionService, err := application.NewDecisionService(decisionRepository, source, generator)
	if err != nil {
		return nil, err
	}
	activityService, err := application.NewActivityService(activityRepository)
	if err != nil {
		return nil, err
	}
	searchService, err := application.NewSearchService(searchRepository)
	if err != nil {
		return nil, err
	}
	reviewService, err := application.NewReviewService(reviewRepository, issueRepository, source, generator)
	if err != nil {
		return nil, err
	}
	attemptService, err := application.NewAttemptService(attemptRepository, source, generator)
	if err != nil {
		return nil, err
	}
	reservationService, err := application.NewReservationService(reservationRepository)
	if err != nil {
		return nil, err
	}
	maintenanceService, err := application.NewMaintenanceService(attemptRepository, searchIndexRepository, source)
	if err != nil {
		return nil, err
	}
	workContextService, err := application.NewWorkContextService(workContextRepository, source)
	if err != nil {
		return nil, err
	}
	sessionRepository, err := sqlite.NewAgentSessionRepository(project.Database)
	if err != nil {
		return nil, err
	}
	sessionService, err := application.NewAgentSessionService(sessionRepository, source, generator)
	if err != nil {
		return nil, err
	}
	workflowPolicyService, err := application.NewWorkflowPolicyService(workflowPolicyRepository, source, generator)
	if err != nil {
		return nil, err
	}
	boardService, err := application.NewBoardService(issueService, attemptService, reservationService, reviewService, graphService, source)
	if err != nil {
		return nil, err
	}
	issueDetailService, err := application.NewIssueDetailService(issueService, graphService, activityService, reservationService)
	if err != nil {
		return nil, err
	}

	return &Services{
		project: project,
		bundle: application.Bundle{
			IssueService:          issueService,
			ProjectService:        projectService,
			RelationService:       relationService,
			GraphService:          graphService,
			PlanningService:       planningService,
			CommentService:        commentService,
			DecisionService:       decisionService,
			ActivityService:       activityService,
			SearchService:         searchService,
			ReviewService:         reviewService,
			AttemptService:        attemptService,
			ReservationService:    reservationService,
			SessionService:        sessionService,
			WorkContextService:    workContextService,
			WorkflowPolicyService: workflowPolicyService,
			MaintenanceService:    maintenanceService,
			BoardService:          boardService,
			IssueDetailService:    issueDetailService,
		},
	}, nil
}

// Open opens a new project from a starting path.
func Open(ctx context.Context, startingPath string, pathInputs projectconfig.PathInputs, dataRootOverride string) (*Services, *runtime.Project, error) {
	project, err := openProject(ctx, startingPath, pathInputs, dataRootOverride)
	if err != nil {
		return nil, nil, err
	}
	openedProject := project
	keepProject := false
	defer func() {
		if keepProject {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := openedProject.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	bundle, err := NewServices(project, nil)
	if err != nil {
		return nil, nil, err
	}
	keepProject = true
	return bundle, project, nil
}

// OpenExisting opens an existing project given its ID and data root.
func OpenExisting(ctx context.Context, projectID, dataRoot string, source clock.Clock, sqliteOptions sqlite.Options) (*Services, *runtime.Project, error) {
	if source == nil {
		source = clock.RealClock{}
	}
	project, err := runtime.OpenExistingProject(ctx, projectID, dataRoot, source, sqliteOptions)
	if err != nil {
		return nil, nil, err
	}
	openedProject := project
	keepProject := false
	defer func() {
		if keepProject {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := openedProject.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	bundle, err := NewServices(project, source)
	if err != nil {
		return nil, nil, err
	}
	keepProject = true
	return bundle, project, nil
}

func openProject(ctx context.Context, startingPath string, pathInputs projectconfig.PathInputs, dataRootOverride string) (*runtime.Project, error) {
	options := runtime.Options{
		StartingPath: startingPath,
		PathInputs:   pathInputs,
		Clock:        clock.RealClock{},
		SQLite:       sqlite.Options{},
	}
	if dataRootOverride != "" {
		options.DataRoot = dataRootOverride
	}
	return runtime.OpenProject(ctx, options)
}
