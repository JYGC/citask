package reportservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/JYGC/citask/internal/domain"
)

const (
	exampleCommitSha     = "094b1e7c00000000000000000000000000000000"
	exampleRunIdentifier = "20261031T052700Z-094b1e7"
)

var exampleProject = domain.Project{Name: "example-project", TrunkBranch: "master"}

type publishedStatus struct {
	CommitSha string
	Status    domain.CommitStatus
}

type recordingGitHub struct {
	publishError       error
	findError          error
	openError          error
	commentError       error
	closeError         error
	failFromResultSha  string
	publishedStatuses  []publishedStatus
	openIssueNumber    int
	issueBodies        []string
	comments           []string
	closingComments    []string
	closedIssueNumbers []int
}

func (github *recordingGitHub) PublishCommitStatus(ctx context.Context, project domain.Project, commitSha string, status domain.CommitStatus) error {
	if github.publishError != nil && (github.failFromResultSha == "" || github.failFromResultSha == commitSha) {
		return github.publishError
	}
	github.publishedStatuses = append(github.publishedStatuses, publishedStatus{CommitSha: commitSha, Status: status})
	return nil
}

func (github *recordingGitHub) FindOpenFailureIssue(ctx context.Context, project domain.Project) (int, bool, error) {
	return github.openIssueNumber, github.openIssueNumber != 0, github.findError
}

func (github *recordingGitHub) OpenFailureIssue(ctx context.Context, project domain.Project, body string) error {
	if github.openError != nil {
		return github.openError
	}
	github.issueBodies = append(github.issueBodies, body)
	github.openIssueNumber = 7
	return nil
}

func (github *recordingGitHub) CommentOnFailureIssue(ctx context.Context, project domain.Project, issueNumber int, body string) error {
	if github.commentError != nil {
		return github.commentError
	}
	github.comments = append(github.comments, body)
	return nil
}

func (github *recordingGitHub) CloseFailureIssue(ctx context.Context, project domain.Project, issueNumber int, closingComment string) error {
	if github.closeError != nil {
		return github.closeError
	}
	github.closingComments = append(github.closingComments, closingComment)
	github.closedIssueNumbers = append(github.closedIssueNumbers, issueNumber)
	github.openIssueNumber = 0
	return nil
}

type inMemoryUnreportedResults struct {
	results   []domain.RunResult
	listError error
}

func (unreported *inMemoryUnreportedResults) SaveUnreportedResult(result domain.RunResult) error {
	unreported.results = append(unreported.results, result)
	return nil
}

func (unreported *inMemoryUnreportedResults) ListUnreportedResultsOldestFirst(project domain.Project) ([]domain.RunResult, error) {
	return append([]domain.RunResult{}, unreported.results...), unreported.listError
}

func (unreported *inMemoryUnreportedResults) DeleteUnreportedResult(result domain.RunResult) error {
	for resultIndex, candidate := range unreported.results {
		if candidate.RunIdentifier == result.RunIdentifier {
			unreported.results = append(unreported.results[:resultIndex], unreported.results[resultIndex+1:]...)
			return nil
		}
	}
	return errors.New("no such result")
}

type recordingRunLog struct {
	textByRunIdentifier map[string]string
	appendError         error
}

func (runLog *recordingRunLog) CreateRunLog(project domain.Project, runIdentifier string) (domain.RunLogLocation, error) {
	return domain.RunLogLocation{}, nil
}

func (runLog *recordingRunLog) AppendToRunLog(project domain.Project, runIdentifier, text string) error {
	if runLog.appendError != nil {
		return runLog.appendError
	}
	runLog.textByRunIdentifier[runIdentifier] += text
	return nil
}

func (runLog *recordingRunLog) RecordPollProblem(project domain.Project, problem string) error {
	return nil
}

func (runLog *recordingRunLog) DeleteRunLogsOlderThan(project domain.Project, cutoff time.Time) error {
	return nil
}

type reportTestFixture struct {
	github     *recordingGitHub
	unreported *inMemoryUnreportedResults
	runLog     *recordingRunLog
	service    *ReportService
}

func newReportTestFixture(results ...domain.RunResult) *reportTestFixture {
	fixture := &reportTestFixture{
		github:     &recordingGitHub{},
		unreported: &inMemoryUnreportedResults{results: results},
		runLog:     &recordingRunLog{textByRunIdentifier: map[string]string{}},
	}
	fixture.service = New(Dependencies{
		CommitStatusPublisher:      fixture.github,
		FailureIssueTracker:        fixture.github,
		UnreportedResultRepository: fixture.unreported,
		RunLogRepository:           fixture.runLog,
	})
	return fixture
}

type countingHeartbeatSender struct {
	calls int
	err   error
}

func (sender *countingHeartbeatSender) SendHeartbeat(ctx context.Context) error {
	sender.calls++
	return sender.err
}

func (fixture *reportTestFixture) withHeartbeatSender(sender *countingHeartbeatSender) *ReportService {
	dependencies := fixture.service.Dependencies
	dependencies.HeartbeatSender = sender
	return New(dependencies)
}

func (fixture *reportTestFixture) reportEverything(t *testing.T) {
	t.Helper()
	if err := fixture.service.ReportUnreportedResults(context.Background(), exampleProject); err != nil {
		t.Fatal(err)
	}
}

func passingResult(commitSha, runIdentifier string) domain.RunResult {
	return domain.RunResult{
		ProjectName:   exampleProject.Name,
		CommitSha:     commitSha,
		RunIdentifier: runIdentifier,
		Passed:        true,
		StepOutcomes:  []domain.StepOutcome{{StepName: "go-tests", Status: domain.StepPassed}},
	}
}

func failingResult(commitSha, runIdentifier string) domain.RunResult {
	return domain.RunResult{
		ProjectName:   exampleProject.Name,
		CommitSha:     commitSha,
		RunIdentifier: runIdentifier,
		Passed:        false,
		StepOutcomes: []domain.StepOutcome{
			{StepName: "build", Status: domain.StepPassed},
			{StepName: "go-tests", Status: domain.StepFailed},
			{StepName: "e2e-tests", Status: domain.StepStoppedByRunTimeLimit},
			{StepName: "package", Status: domain.StepSkipped},
		},
	}
}

func TestFailureWithNoOpenIssueOpensOne(t *testing.T) {
	fixture := newReportTestFixture(failingResult(exampleCommitSha, exampleRunIdentifier))

	fixture.reportEverything(t)

	if len(fixture.github.issueBodies) != 1 || len(fixture.github.comments) != 0 {
		t.Errorf("issues %v, comments %v", fixture.github.issueBodies, fixture.github.comments)
	}
}

func TestFailureWithAnOpenIssueCommentsOnIt(t *testing.T) {
	fixture := newReportTestFixture(failingResult(exampleCommitSha, exampleRunIdentifier))
	fixture.github.openIssueNumber = 7

	fixture.reportEverything(t)

	if len(fixture.github.issueBodies) != 0 || len(fixture.github.comments) != 1 {
		t.Errorf("issues %v, comments %v", fixture.github.issueBodies, fixture.github.comments)
	}
}

func TestPassWithAnOpenIssueClosesItNamingThePassingCommit(t *testing.T) {
	fixture := newReportTestFixture(passingResult(exampleCommitSha, exampleRunIdentifier))
	fixture.github.openIssueNumber = 7

	fixture.reportEverything(t)

	if len(fixture.github.closedIssueNumbers) != 1 || fixture.github.closedIssueNumbers[0] != 7 {
		t.Fatalf("closed issues: %v", fixture.github.closedIssueNumbers)
	}
	if !strings.Contains(fixture.github.closingComments[0], exampleCommitSha) {
		t.Errorf("closing comment: %q", fixture.github.closingComments[0])
	}
}

func TestPassWithNoOpenIssueDoesNothingMoreThanTheStatus(t *testing.T) {
	fixture := newReportTestFixture(passingResult(exampleCommitSha, exampleRunIdentifier))

	fixture.reportEverything(t)

	if len(fixture.github.issueBodies)+len(fixture.github.comments)+len(fixture.github.closingComments) != 0 {
		t.Errorf("nothing but the status should be posted: %+v", fixture.github)
	}
	if len(fixture.github.publishedStatuses) != 1 {
		t.Errorf("statuses: %+v", fixture.github.publishedStatuses)
	}
}

func TestReportContainsOnlyTheAllowedFields(t *testing.T) {
	fixture := newReportTestFixture(failingResult(exampleCommitSha, exampleRunIdentifier))

	fixture.reportEverything(t)

	expectedBody := "CI failed.\n\n" +
		"Project: example-project\n" +
		"Commit: " + exampleCommitSha + "\n" +
		"Run: " + exampleRunIdentifier + "\n" +
		"Steps that did not pass:\n" +
		"- go-tests: failed\n" +
		"- e2e-tests: stopped-by-run-time-limit\n"
	if fixture.github.issueBodies[0] != expectedBody {
		t.Errorf("got:\n%s\nwant:\n%s", fixture.github.issueBodies[0], expectedBody)
	}
}

func TestCommitStatusNamesTheOutcomeAndSkippedStepsAreNotCalledFailures(t *testing.T) {
	fixture := newReportTestFixture(
		failingResult("failing-commit", "20261031T052700Z-aaaaaaa"),
		passingResult("passing-commit", "20261031T053200Z-bbbbbbb"),
	)

	fixture.reportEverything(t)

	failed := fixture.github.publishedStatuses[0]
	if failed.CommitSha != "failing-commit" || failed.Status.State != domain.CommitStatusFailure {
		t.Errorf("failed status: %+v", failed)
	}
	if failed.Status.Description != "Did not pass: go-tests, e2e-tests" {
		t.Errorf("description: %q", failed.Status.Description)
	}
	passed := fixture.github.publishedStatuses[1]
	if passed.CommitSha != "passing-commit" || passed.Status.State != domain.CommitStatusSuccess {
		t.Errorf("passed status: %+v", passed)
	}
}

func TestLongCommitStatusDescriptionIsCutToGitHubsLimitByCharacter(t *testing.T) {
	result := failingResult(exampleCommitSha, exampleRunIdentifier)
	result.StepOutcomes = []domain.StepOutcome{{StepName: strings.Repeat("é", 200), Status: domain.StepFailed}}
	fixture := newReportTestFixture(result)

	fixture.reportEverything(t)

	description := fixture.github.publishedStatuses[0].Status.Description
	if length := utf8.RuneCountInString(description); length != 140 {
		t.Errorf("description has %d characters", length)
	}
	if !utf8.ValidString(description) {
		t.Error("the description was cut through a character")
	}
}

func TestResultsAreReportedOldestFirstAndForgottenOnceReported(t *testing.T) {
	fixture := newReportTestFixture(
		failingResult("older-commit", "20261031T052700Z-aaaaaaa"),
		passingResult("newer-commit", "20261031T053200Z-bbbbbbb"),
	)

	fixture.reportEverything(t)

	statuses := fixture.github.publishedStatuses
	if len(statuses) != 2 || statuses[0].CommitSha != "older-commit" || statuses[1].CommitSha != "newer-commit" {
		t.Fatalf("statuses: %+v", statuses)
	}
	if len(fixture.unreported.results) != 0 {
		t.Errorf("results should be forgotten: %+v", fixture.unreported.results)
	}
	if len(fixture.github.closedIssueNumbers) != 1 {
		t.Errorf("the pass should close the issue the failure opened: %+v", fixture.github)
	}
}

func TestReportingStopsAtTheFirstFailureSoLaterResultsAreNotPostedAheadOfIt(t *testing.T) {
	fixture := newReportTestFixture(
		failingResult("older-commit", "20261031T052700Z-aaaaaaa"),
		passingResult("newer-commit", "20261031T053200Z-bbbbbbb"),
	)
	fixture.github.publishError = errors.New("GitHub answered 502")

	fixture.reportEverything(t)

	if len(fixture.unreported.results) != 2 {
		t.Errorf("both results should be kept: %+v", fixture.unreported.results)
	}
	if !strings.Contains(fixture.runLog.textByRunIdentifier["20261031T052700Z-aaaaaaa"], "GitHub answered 502") {
		t.Errorf("the failure should be in the run log of that run: %v", fixture.runLog.textByRunIdentifier)
	}
	if fixture.runLog.textByRunIdentifier["20261031T053200Z-bbbbbbb"] != "" {
		t.Errorf("the newer run was not attempted: %v", fixture.runLog.textByRunIdentifier)
	}
}

func TestEachPartOfTheIssueLifecycleKeepsTheResultWhenItFails(t *testing.T) {
	failures := map[string]struct {
		configure func(github *recordingGitHub)
		result    domain.RunResult
	}{
		"finding the issue": {
			configure: func(github *recordingGitHub) { github.findError = errors.New("GitHub answered 502") },
			result:    failingResult(exampleCommitSha, exampleRunIdentifier),
		},
		"opening the issue": {
			configure: func(github *recordingGitHub) { github.openError = errors.New("GitHub answered 502") },
			result:    failingResult(exampleCommitSha, exampleRunIdentifier),
		},
		"commenting on the issue": {
			configure: func(github *recordingGitHub) {
				github.openIssueNumber = 7
				github.commentError = errors.New("GitHub answered 502")
			},
			result: failingResult(exampleCommitSha, exampleRunIdentifier),
		},
		"closing the issue": {
			configure: func(github *recordingGitHub) {
				github.openIssueNumber = 7
				github.closeError = errors.New("GitHub answered 502")
			},
			result: passingResult(exampleCommitSha, exampleRunIdentifier),
		},
	}
	for failureName, failure := range failures {
		t.Run(failureName, func(t *testing.T) {
			fixture := newReportTestFixture(failure.result)
			failure.configure(fixture.github)

			fixture.reportEverything(t)

			if len(fixture.unreported.results) != 1 {
				t.Errorf("the result should be kept for replay: %+v", fixture.unreported.results)
			}
		})
	}
}

func TestResultsThatCannotBeListedAreReturnedAsAnError(t *testing.T) {
	fixture := newReportTestFixture()
	fixture.unreported.listError = errors.New("disk failure")

	if err := fixture.service.ReportUnreportedResults(context.Background(), exampleProject); err == nil {
		t.Fatal("a state failure must reach cron")
	}
}

func TestReportingFailureThatCannotBeLoggedIsReturnedAsAnError(t *testing.T) {
	fixture := newReportTestFixture(passingResult(exampleCommitSha, exampleRunIdentifier))
	fixture.github.publishError = errors.New("GitHub answered 502")
	fixture.runLog.appendError = errors.New("disk full")

	if err := fixture.service.ReportUnreportedResults(context.Background(), exampleProject); err == nil {
		t.Fatal("a state failure must reach cron")
	}
}

func TestRunStartPostsAPendingStatusOnTheCommitUnderTest(t *testing.T) {
	fixture := newReportTestFixture()

	err := fixture.service.ReportRunStarted(context.Background(), exampleProject, domain.PreparedRun{CommitSha: exampleCommitSha, RunIdentifier: exampleRunIdentifier})

	if err != nil {
		t.Fatal(err)
	}
	statuses := fixture.github.publishedStatuses
	if len(statuses) != 1 || statuses[0].CommitSha != exampleCommitSha || statuses[0].Status.State != domain.CommitStatusPending {
		t.Errorf("statuses: %+v", statuses)
	}
}

func TestPendingStatusThatCannotBePostedIsLoggedAndTheRunContinues(t *testing.T) {
	fixture := newReportTestFixture()
	fixture.github.publishError = errors.New("GitHub answered 502")

	err := fixture.service.ReportRunStarted(context.Background(), exampleProject, domain.PreparedRun{CommitSha: exampleCommitSha, RunIdentifier: exampleRunIdentifier})

	if err != nil {
		t.Fatalf("a reporting problem must not stop the run: %v", err)
	}
	if !strings.Contains(fixture.runLog.textByRunIdentifier[exampleRunIdentifier], "GitHub answered 502") {
		t.Errorf("the failure should be in the run log: %v", fixture.runLog.textByRunIdentifier)
	}
}

func TestHeartbeatIsSentWhenNoResultIsWaiting(t *testing.T) {
	fixture := newReportTestFixture()
	sender := &countingHeartbeatSender{}

	if err := fixture.withHeartbeatSender(sender).SendHeartbeatUnlessResultsAreWaiting(context.Background(), []domain.Project{exampleProject}); err != nil {
		t.Fatal(err)
	}

	if sender.calls != 1 {
		t.Errorf("heartbeats: %d", sender.calls)
	}
}

func TestHeartbeatIsWithheldWhileAResultIsWaiting(t *testing.T) {
	fixture := newReportTestFixture(passingResult(exampleCommitSha, exampleRunIdentifier))
	sender := &countingHeartbeatSender{}

	if err := fixture.withHeartbeatSender(sender).SendHeartbeatUnlessResultsAreWaiting(context.Background(), []domain.Project{exampleProject}); err != nil {
		t.Fatal(err)
	}

	if sender.calls != 0 {
		t.Errorf("heartbeats: %d", sender.calls)
	}
}

func TestHeartbeatThatCannotBeSentIsNotAnError(t *testing.T) {
	fixture := newReportTestFixture()
	sender := &countingHeartbeatSender{err: errors.New("service unreachable")}

	if err := fixture.withHeartbeatSender(sender).SendHeartbeatUnlessResultsAreWaiting(context.Background(), []domain.Project{exampleProject}); err != nil {
		t.Fatalf("a heartbeat problem must not reach cron: %v", err)
	}
}

func TestResultsThatCannotBeCheckedForAHeartbeatAreAnError(t *testing.T) {
	fixture := newReportTestFixture()
	fixture.unreported.listError = errors.New("disk failure")
	sender := &countingHeartbeatSender{}

	if err := fixture.withHeartbeatSender(sender).SendHeartbeatUnlessResultsAreWaiting(context.Background(), []domain.Project{exampleProject}); err == nil {
		t.Fatal("a state failure must reach cron")
	}
	if sender.calls != 0 {
		t.Errorf("heartbeats: %d", sender.calls)
	}
}
