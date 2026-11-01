package runservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

var exampleProject = domain.Project{
	Name:         "example-project",
	TrunkBranch:  "master",
	RunTimeLimit: time.Minute,
}

var exampleRun = domain.PreparedRun{
	CommitSha:     "094b1e7c00000000000000000000000000000000",
	RunIdentifier: "20261031T052700Z-094b1e7",
	StepEnvironment: domain.StepEnvironment{
		CheckoutDirectory:      "/example/checkout",
		PrivateTemporaryFolder: "/example/temporary",
	},
}

type scriptedStepProcessRunner struct {
	resultsOfEachAttempt map[string][]error
	attemptedStepNames   []string
	afterEachAttempt     func(stepName string)
}

func (runner *scriptedStepProcessRunner) RunStep(ctx context.Context, step domain.StepDefinition, environment domain.StepEnvironment) error {
	runner.attemptedStepNames = append(runner.attemptedStepNames, step.Name)
	if runner.afterEachAttempt != nil {
		defer runner.afterEachAttempt(step.Name)
	}
	resultsLeft := runner.resultsOfEachAttempt[step.Name]
	if len(resultsLeft) == 0 {
		return nil
	}
	runner.resultsOfEachAttempt[step.Name] = resultsLeft[1:]
	return resultsLeft[0]
}

type fixedStepDefinitionReader struct {
	steps []domain.StepDefinition
	err   error
}

func (reader fixedStepDefinitionReader) ReadStepDefinitions(checkoutDirectory string) ([]domain.StepDefinition, error) {
	return reader.steps, reader.err
}

type inMemoryRunLog struct {
	text          strings.Builder
	appendError   error
	pollProblems  []string
	deletedBefore time.Time
}

func (runLog *inMemoryRunLog) CreateRunLog(project domain.Project, runIdentifier string) (domain.RunLogLocation, error) {
	return domain.RunLogLocation{RunLogFile: "/example/run.log", RunArtifactDirectory: "/example/artifacts"}, nil
}

func (runLog *inMemoryRunLog) AppendToRunLog(project domain.Project, runIdentifier, text string) error {
	if runLog.appendError != nil {
		return runLog.appendError
	}
	runLog.text.WriteString(text)
	return nil
}

func (runLog *inMemoryRunLog) RecordPollProblem(project domain.Project, problem string) error {
	runLog.pollProblems = append(runLog.pollProblems, problem)
	return nil
}

func (runLog *inMemoryRunLog) DeleteRunLogsOlderThan(project domain.Project, cutoff time.Time) error {
	runLog.deletedBefore = cutoff
	return nil
}

type recordingCleanup struct {
	eventsInOrder     []string
	stopperError      error
	deleteFolderError error
}

func (cleanup *recordingCleanup) StopLeftoverProcesses() error {
	cleanup.eventsInOrder = append(cleanup.eventsInOrder, "stop-leftover-processes")
	return cleanup.stopperError
}

func (cleanup *recordingCleanup) CreatePrivateTemporaryFolder(project domain.Project, commitSha string) (string, error) {
	return "/example/temporary", nil
}

func (cleanup *recordingCleanup) DeletePrivateTemporaryFolder(folder string) error {
	cleanup.eventsInOrder = append(cleanup.eventsInOrder, "delete-private-temporary-folder:"+folder)
	return cleanup.deleteFolderError
}

func step(name string, dependsOn ...string) domain.StepDefinition {
	return domain.StepDefinition{Name: name, WorkingDirectory: ".", Command: []string{"true"}, DependsOn: dependsOn}
}

func repeatingStep(name string) domain.StepDefinition {
	definition := step(name)
	definition.RepeatOnceOnFailure = true
	return definition
}

type runTestFixture struct {
	runner  *scriptedStepProcessRunner
	runLog  *inMemoryRunLog
	cleanup *recordingCleanup
	service *RunService
}

func newRunTestFixture(steps []domain.StepDefinition, resultsOfEachAttempt map[string][]error) *runTestFixture {
	fixture := &runTestFixture{
		runner:  &scriptedStepProcessRunner{resultsOfEachAttempt: resultsOfEachAttempt},
		runLog:  &inMemoryRunLog{},
		cleanup: &recordingCleanup{},
	}
	fixture.service = New(Dependencies{
		StepDefinitionReader:   fixedStepDefinitionReader{steps: steps},
		StepProcessRunner:      fixture.runner,
		RunLogRepository:       fixture.runLog,
		TemporaryFolders:       fixture.cleanup,
		LeftoverProcessStopper: fixture.cleanup,
	})
	return fixture
}

func (fixture *runTestFixture) executeRun(t *testing.T) domain.RunResult {
	t.Helper()
	result, err := fixture.service.ExecutePreparedRun(context.Background(), exampleProject, exampleRun)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func statusesByStepName(result domain.RunResult) map[string]domain.StepStatus {
	statuses := map[string]domain.StepStatus{}
	for _, outcome := range result.StepOutcomes {
		statuses[outcome.StepName] = outcome.Status
	}
	return statuses
}

func assertStepStatus(t *testing.T, result domain.RunResult, stepName string, want domain.StepStatus) {
	t.Helper()
	if got := statusesByStepName(result)[stepName]; got != want {
		t.Errorf("status of %s: got %q, want %q", stepName, got, want)
	}
}

func assertAttempts(t *testing.T, fixture *runTestFixture, want ...string) {
	t.Helper()
	if strings.Join(fixture.runner.attemptedStepNames, ",") != strings.Join(want, ",") {
		t.Errorf("attempted steps: got %v, want %v", fixture.runner.attemptedStepNames, want)
	}
}

var errExampleStepFailed = errors.New("exit status 1")

func TestAllStepsPassingPassesTheRunAndRunsThemInFileOrder(t *testing.T) {
	fixture := newRunTestFixture([]domain.StepDefinition{step("first"), step("second", "first"), step("third")}, nil)

	result := fixture.executeRun(t)

	if !result.Passed {
		t.Error("the run should pass")
	}
	assertAttempts(t, fixture, "first", "second", "third")
	assertStepStatus(t, result, "second", domain.StepPassed)
	if result.ProjectName != exampleProject.Name || result.CommitSha != exampleRun.CommitSha || result.RunIdentifier != exampleRun.RunIdentifier {
		t.Errorf("result does not identify the run: %+v", result)
	}
	if result.FinishedAt.IsZero() {
		t.Error("the result should record when it finished")
	}
}

func TestFailedStepSkipsItsDependentsAndIndependentStepsStillRun(t *testing.T) {
	fixture := newRunTestFixture(
		[]domain.StepDefinition{step("build"), step("test", "build"), step("package", "test"), step("lint")},
		map[string][]error{"build": {errExampleStepFailed}},
	)

	result := fixture.executeRun(t)

	if result.Passed {
		t.Error("the run should fail")
	}
	assertAttempts(t, fixture, "build", "lint")
	assertStepStatus(t, result, "build", domain.StepFailed)
	assertStepStatus(t, result, "test", domain.StepSkipped)
	assertStepStatus(t, result, "package", domain.StepSkipped)
	assertStepStatus(t, result, "lint", domain.StepPassed)
}

func TestStepDependingOnSeveralStepsIsSkippedIfAnyOfThemDidNotPass(t *testing.T) {
	fixture := newRunTestFixture(
		[]domain.StepDefinition{step("first"), step("second"), step("needs-both", "first", "second")},
		map[string][]error{"second": {errExampleStepFailed}},
	)

	result := fixture.executeRun(t)

	assertStepStatus(t, result, "needs-both", domain.StepSkipped)
}

func TestStepThatPassedOnRepeatStillSatisfiesItsDependents(t *testing.T) {
	fixture := newRunTestFixture(
		[]domain.StepDefinition{repeatingStep("flaky"), step("dependent", "flaky")},
		map[string][]error{"flaky": {errExampleStepFailed, nil}},
	)

	result := fixture.executeRun(t)

	if !result.Passed {
		t.Error("the run should pass")
	}
	assertAttempts(t, fixture, "flaky", "flaky", "dependent")
	assertStepStatus(t, result, "flaky", domain.StepPassedOnRepeat)
	assertStepStatus(t, result, "dependent", domain.StepPassed)
	if !strings.Contains(fixture.runLog.text.String(), string(domain.StepPassedOnRepeat)) {
		t.Errorf("the repeat should be visible in the run log:\n%s", fixture.runLog.text.String())
	}
}

func TestStepThatFailsItsRepeatFailsTheRun(t *testing.T) {
	fixture := newRunTestFixture(
		[]domain.StepDefinition{repeatingStep("broken")},
		map[string][]error{"broken": {errExampleStepFailed, errExampleStepFailed}},
	)

	result := fixture.executeRun(t)

	if result.Passed {
		t.Error("the run should fail")
	}
	assertAttempts(t, fixture, "broken", "broken")
	assertStepStatus(t, result, "broken", domain.StepFailed)
}

func TestStepWithoutRepeatIsAttemptedOnce(t *testing.T) {
	fixture := newRunTestFixture([]domain.StepDefinition{step("once")}, map[string][]error{"once": {errExampleStepFailed}})

	fixture.executeRun(t)

	assertAttempts(t, fixture, "once")
}

func TestStepStoppedByTheTimeLimitIsNotRepeatedAndSkipsEveryLaterStep(t *testing.T) {
	stoppedStep := repeatingStep("hanging")
	fixture := newRunTestFixture(
		[]domain.StepDefinition{stoppedStep, step("independent")},
		map[string][]error{"hanging": {domain.ErrStepStoppedByRunTimeLimit}},
	)

	result := fixture.executeRun(t)

	if result.Passed {
		t.Error("the run should fail")
	}
	assertAttempts(t, fixture, "hanging")
	assertStepStatus(t, result, "hanging", domain.StepStoppedByRunTimeLimit)
	assertStepStatus(t, result, "independent", domain.StepSkipped)
}

func TestRepeatStoppedByTheTimeLimitIsReportedAsStopped(t *testing.T) {
	fixture := newRunTestFixture(
		[]domain.StepDefinition{repeatingStep("hanging-on-repeat")},
		map[string][]error{"hanging-on-repeat": {errExampleStepFailed, domain.ErrStepStoppedByRunTimeLimit}},
	)

	result := fixture.executeRun(t)

	assertStepStatus(t, result, "hanging-on-repeat", domain.StepStoppedByRunTimeLimit)
}

func TestTimeLimitPassingBetweenStepsSkipsEveryLaterStep(t *testing.T) {
	fixture := newRunTestFixture([]domain.StepDefinition{step("slow"), step("later")}, nil)
	shortLimitProject := exampleProject
	shortLimitProject.RunTimeLimit = 20 * time.Millisecond
	fixture.runner.afterEachAttempt = func(stepName string) { time.Sleep(60 * time.Millisecond) }

	result, err := fixture.service.ExecutePreparedRun(context.Background(), shortLimitProject, exampleRun)

	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Error("a run whose later steps did not run must not pass")
	}
	assertAttempts(t, fixture, "slow")
	assertStepStatus(t, result, "later", domain.StepSkipped)
}

func TestUnreadableStepDefinitionsFailTheRunAtThatNamedStepAndLogTheReason(t *testing.T) {
	fixture := newRunTestFixture(nil, nil)
	fixture.service = New(Dependencies{
		StepDefinitionReader:   fixedStepDefinitionReader{err: errors.New("unknown field DependOn")},
		StepProcessRunner:      fixture.runner,
		RunLogRepository:       fixture.runLog,
		TemporaryFolders:       fixture.cleanup,
		LeftoverProcessStopper: fixture.cleanup,
	})

	result := fixture.executeRun(t)

	if result.Passed {
		t.Error("the run should fail")
	}
	assertStepStatus(t, result, domain.ReadStepDefinitionsStepName, domain.StepFailed)
	if !strings.Contains(fixture.runLog.text.String(), "unknown field DependOn") {
		t.Errorf("the reason should be in the run log:\n%s", fixture.runLog.text.String())
	}
}

func TestLeftoverProcessesAreStoppedBeforeThePrivateFolderIsDeletedWhateverTheOutcome(t *testing.T) {
	results := map[string]map[string][]error{
		"pass":       nil,
		"failure":    {"only-step": {errExampleStepFailed}},
		"time limit": {"only-step": {domain.ErrStepStoppedByRunTimeLimit}},
	}
	for outcomeName, resultsOfEachAttempt := range results {
		t.Run(outcomeName, func(t *testing.T) {
			fixture := newRunTestFixture([]domain.StepDefinition{step("only-step")}, resultsOfEachAttempt)

			fixture.executeRun(t)

			want := "stop-leftover-processes,delete-private-temporary-folder:/example/temporary"
			if got := strings.Join(fixture.cleanup.eventsInOrder, ","); got != want {
				t.Errorf("got %s, want %s", got, want)
			}
		})
	}
}

func TestCleanupProblemsAreLoggedAndDoNotChangeTheOutcome(t *testing.T) {
	fixture := newRunTestFixture([]domain.StepDefinition{step("only-step")}, nil)
	fixture.cleanup.stopperError = errors.New("refused to stop processes")
	fixture.cleanup.deleteFolderError = errors.New("could not delete folder")

	result := fixture.executeRun(t)

	if !result.Passed {
		t.Error("cleanup problems should not fail a passing run")
	}
	runLog := fixture.runLog.text.String()
	for _, expected := range []string{"refused to stop processes", "could not delete folder"} {
		if !strings.Contains(runLog, expected) {
			t.Errorf("expected %q in the run log:\n%s", expected, runLog)
		}
	}
}

func TestRunLogThatCannotBeWrittenIsReturnedAfterCleanup(t *testing.T) {
	fixture := newRunTestFixture([]domain.StepDefinition{step("only-step")}, nil)
	fixture.runLog.appendError = errors.New("disk full")

	_, err := fixture.service.ExecutePreparedRun(context.Background(), exampleProject, exampleRun)

	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("expected the log failure to be returned, got %v", err)
	}
	if len(fixture.cleanup.eventsInOrder) != 2 {
		t.Errorf("cleanup must still happen: %v", fixture.cleanup.eventsInOrder)
	}
}

type fixedTrunkCommitReader struct {
	commitSha string
	err       error
}

func (reader fixedTrunkCommitReader) ReadCurrentTrunkCommit(ctx context.Context, project domain.Project) (string, error) {
	return reader.commitSha, reader.err
}

type inMemoryLastTestedCommit struct {
	commitSha string
	readError error
	saveError error
}

func (lastTested *inMemoryLastTestedCommit) ReadLastTestedCommit(project domain.Project) (string, bool, error) {
	return lastTested.commitSha, lastTested.commitSha != "", lastTested.readError
}

func (lastTested *inMemoryLastTestedCommit) SaveLastTestedCommit(project domain.Project, commitSha string) error {
	if lastTested.saveError != nil {
		return lastTested.saveError
	}
	lastTested.commitSha = commitSha
	return nil
}

func TestTrunkCommitIsUntestedWhenItDiffersFromTheLastTestedCommit(t *testing.T) {
	service := New(Dependencies{
		TrunkCommitReader:          fixedTrunkCommitReader{commitSha: "new-commit"},
		LastTestedCommitRepository: &inMemoryLastTestedCommit{commitSha: "old-commit"},
	})

	commitSha, found, err := service.FindUntestedTrunkCommit(context.Background(), exampleProject)

	if err != nil {
		t.Fatal(err)
	}
	if !found || commitSha != "new-commit" {
		t.Errorf("got %q, found %v", commitSha, found)
	}
}

func TestTrunkCommitIsUntestedWhenNothingHasBeenTested(t *testing.T) {
	service := New(Dependencies{
		TrunkCommitReader:          fixedTrunkCommitReader{commitSha: "new-commit"},
		LastTestedCommitRepository: &inMemoryLastTestedCommit{},
	})

	_, found, err := service.FindUntestedTrunkCommit(context.Background(), exampleProject)

	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("a project with no last tested commit tests its current trunk commit")
	}
}

func TestTrunkCommitMatchingTheLastTestedCommitIsNotUntested(t *testing.T) {
	service := New(Dependencies{
		TrunkCommitReader:          fixedTrunkCommitReader{commitSha: "same-commit"},
		LastTestedCommitRepository: &inMemoryLastTestedCommit{commitSha: "same-commit"},
	})

	_, found, err := service.FindUntestedTrunkCommit(context.Background(), exampleProject)

	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("an unchanged commit should not be tested again")
	}
}

func TestRemoteFailureIsReportedAsAProjectThatIsUnavailable(t *testing.T) {
	service := New(Dependencies{
		TrunkCommitReader:          fixedTrunkCommitReader{err: errors.New("could not resolve host")},
		LastTestedCommitRepository: &inMemoryLastTestedCommit{},
	})

	_, _, err := service.FindUntestedTrunkCommit(context.Background(), exampleProject)

	if !errors.Is(err, ErrProjectUnavailable) {
		t.Fatalf("expected ErrProjectUnavailable, got %v", err)
	}
}

func TestStateFailureIsNotMistakenForAProjectThatIsUnavailable(t *testing.T) {
	service := New(Dependencies{
		TrunkCommitReader:          fixedTrunkCommitReader{commitSha: "new-commit"},
		LastTestedCommitRepository: &inMemoryLastTestedCommit{readError: errors.New("disk failure")},
	})

	_, _, err := service.FindUntestedTrunkCommit(context.Background(), exampleProject)

	if err == nil || errors.Is(err, ErrProjectUnavailable) {
		t.Fatalf("a state failure must reach cron, got %v", err)
	}
}

type failingCheckoutPreparer struct{}

func (failingCheckoutPreparer) PrepareCleanCheckoutAtCommit(ctx context.Context, project domain.Project, commitSha string) (string, error) {
	return "", errors.New("fetch failed")
}

func TestCheckoutFailureIsReportedAsAProjectThatIsUnavailable(t *testing.T) {
	service := New(Dependencies{CleanCheckoutPreparer: failingCheckoutPreparer{}})

	_, err := service.PrepareRun(context.Background(), exampleProject, exampleRun.CommitSha)

	if !errors.Is(err, ErrProjectUnavailable) {
		t.Fatalf("expected ErrProjectUnavailable, got %v", err)
	}
}

type orderRecordingUnreportedResults struct {
	lastTested                 *inMemoryLastTestedCommit
	saveError                  error
	wasTestedBeforeResultSaved bool
}

func (results *orderRecordingUnreportedResults) SaveUnreportedResult(result domain.RunResult) error {
	results.wasTestedBeforeResultSaved = results.lastTested.commitSha == result.CommitSha
	return results.saveError
}

func (results *orderRecordingUnreportedResults) ListUnreportedResultsOldestFirst(project domain.Project) ([]domain.RunResult, error) {
	return nil, nil
}

func (results *orderRecordingUnreportedResults) DeleteUnreportedResult(result domain.RunResult) error {
	return nil
}

func TestFinishedRunSavesTheResultBeforeTheLastTestedCommit(t *testing.T) {
	lastTested := &inMemoryLastTestedCommit{}
	results := &orderRecordingUnreportedResults{lastTested: lastTested}
	service := New(Dependencies{UnreportedResultRepository: results, LastTestedCommitRepository: lastTested})

	err := service.RecordFinishedRun(exampleProject, domain.RunResult{CommitSha: "tested-commit"})

	if err != nil {
		t.Fatal(err)
	}
	if results.wasTestedBeforeResultSaved {
		t.Error("the result must be saved first, or a crash between the two loses it")
	}
	if lastTested.commitSha != "tested-commit" {
		t.Errorf("last tested commit: %q", lastTested.commitSha)
	}
}

func TestFinishedRunLeavesTheLastTestedCommitWhenTheResultCannotBeSaved(t *testing.T) {
	lastTested := &inMemoryLastTestedCommit{commitSha: "previous-commit"}
	results := &orderRecordingUnreportedResults{lastTested: lastTested, saveError: errors.New("disk full")}
	service := New(Dependencies{UnreportedResultRepository: results, LastTestedCommitRepository: lastTested})

	err := service.RecordFinishedRun(exampleProject, domain.RunResult{CommitSha: "tested-commit"})

	if err == nil {
		t.Fatal("expected an error")
	}
	if lastTested.commitSha != "previous-commit" {
		t.Errorf("last tested commit should be unchanged, got %q", lastTested.commitSha)
	}
}
