package application

import (
	"context"
	"errors"

	"github.com/JYGC/citask/internal/domain"
	"github.com/JYGC/citask/internal/reportservice"
	"github.com/JYGC/citask/internal/runservice"
)

type PollAllProjects struct {
	projects      []domain.Project
	runService    *runservice.RunService
	reportService *reportservice.ReportService
}

func NewPollAllProjects(projects []domain.Project, runService *runservice.RunService, reportService *reportservice.ReportService) *PollAllProjects {
	return &PollAllProjects{projects: projects, runService: runService, reportService: reportService}
}

// Execute returns an error only for problems that need a person, such as state that cannot be written.
// Cron mails those. A failing project is not one: it is reported on GitHub and the poll exits zero.
func (poll *PollAllProjects) Execute(ctx context.Context) error {
	releaseLock, acquired, err := poll.runService.TryAcquireExclusivePoll()
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer releaseLock()

	var problemsForAPerson []error
	projectsWithUsableState := make([]domain.Project, 0, len(poll.projects))
	for _, project := range poll.projects {
		if err := poll.bringRunRecordsUpToDate(ctx, project); err != nil {
			problemsForAPerson = append(problemsForAPerson, err)
			continue
		}
		projectsWithUsableState = append(projectsWithUsableState, project)
	}
	for _, project := range projectsWithUsableState {
		if err := poll.testTrunkCommitIfUntested(ctx, project); err != nil {
			problemsForAPerson = append(problemsForAPerson, err)
		}
	}
	return errors.Join(problemsForAPerson...)
}

func (poll *PollAllProjects) bringRunRecordsUpToDate(ctx context.Context, project domain.Project) error {
	if err := poll.runService.DeleteExpiredRunRecords(project); err != nil {
		return err
	}
	return poll.reportService.ReportUnreportedResults(ctx, project)
}

func (poll *PollAllProjects) testTrunkCommitIfUntested(ctx context.Context, project domain.Project) error {
	commitSha, found, err := poll.runService.FindUntestedTrunkCommit(ctx, project)
	if errors.Is(err, runservice.ErrProjectUnavailable) {
		return poll.runService.RecordPollProblem(project, err)
	}
	if err != nil || !found {
		return err
	}

	run, err := poll.runService.PrepareRun(ctx, project, commitSha)
	if errors.Is(err, runservice.ErrProjectUnavailable) {
		return poll.runService.RecordPollProblem(project, err)
	}
	if err != nil {
		return err
	}

	if err := poll.reportService.ReportRunStarted(ctx, project, run); err != nil {
		return err
	}
	result, err := poll.runService.ExecutePreparedRun(ctx, project, run)
	if err != nil {
		return err
	}
	if err := poll.runService.RecordFinishedRun(project, result); err != nil {
		return err
	}
	return poll.reportService.ReportUnreportedResults(ctx, project)
}
