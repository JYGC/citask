package reportservice

import (
	"context"
	"fmt"
	"strings"

	"github.com/JYGC/citask/internal/domain"
	"github.com/JYGC/citask/internal/repository"
)

const gitHubCommitStatusDescriptionLimit = 140

type Dependencies struct {
	CommitStatusPublisher      repository.CommitStatusPublisher
	FailureIssueTracker        repository.FailureIssueTracker
	UnreportedResultRepository repository.UnreportedResultRepository
	RunLogRepository           repository.RunLogRepository
	HeartbeatSender            repository.HeartbeatSender
}

type ReportService struct {
	Dependencies
}

func New(dependencies Dependencies) *ReportService {
	return &ReportService{Dependencies: dependencies}
}

func (service *ReportService) ReportRunStarted(ctx context.Context, project domain.Project, run domain.PreparedRun) error {
	pendingStatus := domain.CommitStatus{State: domain.CommitStatusPending, Description: "Running"}
	if err := service.CommitStatusPublisher.PublishCommitStatus(ctx, project, run.CommitSha, pendingStatus); err != nil {
		return service.RunLogRepository.AppendToRunLog(project, run.RunIdentifier, fmt.Sprintf("=== could not post the pending status: %v\n", err))
	}
	return nil
}

// ReportUnreportedResults stops at the first result it cannot post, so a later result can never reach
// GitHub ahead of an earlier one. Only state problems are returned: a GitHub problem is recorded in that
// run's log and the result stays queued for the next poll.
func (service *ReportService) ReportUnreportedResults(ctx context.Context, project domain.Project) error {
	results, err := service.UnreportedResultRepository.ListUnreportedResultsOldestFirst(project)
	if err != nil {
		return err
	}
	for _, result := range results {
		if err := service.reportResult(ctx, project, result); err != nil {
			return service.RunLogRepository.AppendToRunLog(project, result.RunIdentifier, fmt.Sprintf("=== could not report this result: %v\n", err))
		}
		if err := service.UnreportedResultRepository.DeleteUnreportedResult(result); err != nil {
			return err
		}
	}
	return nil
}

// SendHeartbeatUnlessResultsAreWaiting treats a result still waiting as unhealthy, because that is what an
// expired token looks like. A heartbeat that cannot be sent is ignored: it must not affect a run or a
// report, and if the problem lasts, the missing heartbeats raise the alert anyway.
func (service *ReportService) SendHeartbeatUnlessResultsAreWaiting(ctx context.Context, projects []domain.Project) error {
	for _, project := range projects {
		results, err := service.UnreportedResultRepository.ListUnreportedResultsOldestFirst(project)
		if err != nil {
			return err
		}
		if len(results) > 0 {
			return nil
		}
	}
	_ = service.HeartbeatSender.SendHeartbeat(ctx)
	return nil
}

func (service *ReportService) reportResult(ctx context.Context, project domain.Project, result domain.RunResult) error {
	if err := service.CommitStatusPublisher.PublishCommitStatus(ctx, project, result.CommitSha, commitStatusOf(result)); err != nil {
		return err
	}
	issueNumber, issueIsOpen, err := service.FailureIssueTracker.FindOpenFailureIssue(ctx, project)
	if err != nil {
		return err
	}
	switch {
	case result.Passed && issueIsOpen:
		closingComment := fmt.Sprintf("CI passed at commit %s in run %s, so this issue is closed.\n", result.CommitSha, result.RunIdentifier)
		return service.FailureIssueTracker.CloseFailureIssue(ctx, project, issueNumber, closingComment)
	case result.Passed:
		return nil
	case issueIsOpen:
		return service.FailureIssueTracker.CommentOnFailureIssue(ctx, project, issueNumber, failureReport(result))
	default:
		return service.FailureIssueTracker.OpenFailureIssue(ctx, project, failureReport(result))
	}
}

func commitStatusOf(result domain.RunResult) domain.CommitStatus {
	if result.Passed {
		return domain.CommitStatus{State: domain.CommitStatusSuccess, Description: "All steps passed"}
	}
	stepNames := []string{}
	for _, outcome := range stepsThatDidNotPass(result) {
		stepNames = append(stepNames, outcome.StepName)
	}
	return domain.CommitStatus{
		State:       domain.CommitStatusFailure,
		Description: cutToGitHubDescriptionLimit("Did not pass: " + strings.Join(stepNames, ", ")),
	}
}

// failureReport is posted to a public repo, so it holds only what requirements.md allows: the project,
// commit, run identifier, step names and their outcomes. Log output stays on the box.
func failureReport(result domain.RunResult) string {
	var report strings.Builder
	fmt.Fprintf(&report, "CI failed.\n\nProject: %s\nCommit: %s\nRun: %s\nSteps that did not pass:\n", result.ProjectName, result.CommitSha, result.RunIdentifier)
	for _, outcome := range stepsThatDidNotPass(result) {
		fmt.Fprintf(&report, "- %s: %s\n", outcome.StepName, outcome.Status)
	}
	return report.String()
}

func stepsThatDidNotPass(result domain.RunResult) []domain.StepOutcome {
	outcomes := []domain.StepOutcome{}
	for _, outcome := range result.StepOutcomes {
		if outcome.Status == domain.StepFailed || outcome.Status == domain.StepStoppedByRunTimeLimit {
			outcomes = append(outcomes, outcome)
		}
	}
	return outcomes
}

func cutToGitHubDescriptionLimit(description string) string {
	characters := []rune(description)
	if len(characters) <= gitHubCommitStatusDescriptionLimit {
		return description
	}
	return string(characters[:gitHubCommitStatusDescriptionLimit])
}
