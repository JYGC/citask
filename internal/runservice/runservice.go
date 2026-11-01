package runservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JYGC/citask/internal/domain"
	"github.com/JYGC/citask/internal/repository"
)

var ErrProjectUnavailable = errors.New("project unavailable")

type Dependencies struct {
	ExclusivePollLock          repository.ExclusivePollLock
	TrunkCommitReader          repository.TrunkCommitReader
	CleanCheckoutPreparer      repository.CleanCheckoutPreparer
	StepDefinitionReader       repository.StepDefinitionReader
	LastTestedCommitRepository repository.LastTestedCommitRepository
	UnreportedResultRepository repository.UnreportedResultRepository
	RunLogRepository           repository.RunLogRepository
	TemporaryFolders           repository.PrivateTemporaryFolderRepository
	StepProcessRunner          repository.StepProcessRunner
	LeftoverProcessStopper     repository.LeftoverProcessStopper
	RunLogRetention            time.Duration
}

type RunService struct {
	Dependencies
}

func New(dependencies Dependencies) *RunService {
	return &RunService{Dependencies: dependencies}
}

func (service *RunService) TryAcquireExclusivePoll() (releaseLock func() error, acquired bool, err error) {
	return service.ExclusivePollLock.TryAcquireExclusivePollLock()
}

func (service *RunService) RecordPollProblem(project domain.Project, problem error) error {
	return service.RunLogRepository.RecordPollProblem(project, problem.Error())
}

func (service *RunService) DeleteExpiredRunRecords(project domain.Project) error {
	cutoff := time.Now().Add(-service.RunLogRetention)
	unreportedResults, err := service.UnreportedResultRepository.ListUnreportedResultsOldestFirst(project)
	if err != nil {
		return err
	}
	for _, result := range unreportedResults {
		if result.FinishedAt.Before(cutoff) {
			if err := service.UnreportedResultRepository.DeleteUnreportedResult(result); err != nil {
				return err
			}
		}
	}
	return service.RunLogRepository.DeleteRunLogsOlderThan(project, cutoff)
}

func (service *RunService) FindUntestedTrunkCommit(ctx context.Context, project domain.Project) (commitSha string, found bool, err error) {
	currentCommit, err := service.TrunkCommitReader.ReadCurrentTrunkCommit(ctx, project)
	if err != nil {
		return "", false, fmt.Errorf("%w: read trunk commit: %v", ErrProjectUnavailable, err)
	}
	lastTestedCommit, lastTestedCommitExists, err := service.LastTestedCommitRepository.ReadLastTestedCommit(project)
	if err != nil {
		return "", false, err
	}
	if lastTestedCommitExists && lastTestedCommit == currentCommit {
		return "", false, nil
	}
	return currentCommit, true, nil
}

func (service *RunService) PrepareRun(ctx context.Context, project domain.Project, commitSha string) (domain.PreparedRun, error) {
	checkoutDirectory, err := service.CleanCheckoutPreparer.PrepareCleanCheckoutAtCommit(ctx, project, commitSha)
	if err != nil {
		return domain.PreparedRun{}, fmt.Errorf("%w: prepare checkout: %v", ErrProjectUnavailable, err)
	}
	runIdentifier := domain.NewRunIdentifier(time.Now(), commitSha)
	runLogLocation, err := service.RunLogRepository.CreateRunLog(project, runIdentifier)
	if err != nil {
		return domain.PreparedRun{}, err
	}
	privateTemporaryFolder, err := service.TemporaryFolders.CreatePrivateTemporaryFolder(project, commitSha)
	if err != nil {
		return domain.PreparedRun{}, err
	}
	return domain.PreparedRun{
		CommitSha:     commitSha,
		RunIdentifier: runIdentifier,
		StepEnvironment: domain.StepEnvironment{
			CheckoutDirectory:      checkoutDirectory,
			PrivateTemporaryFolder: privateTemporaryFolder,
			RunLogFile:             runLogLocation.RunLogFile,
			RunArtifactDirectory:   runLogLocation.RunArtifactDirectory,
		},
	}, nil
}

func (service *RunService) ExecutePreparedRun(ctx context.Context, project domain.Project, run domain.PreparedRun) (domain.RunResult, error) {
	runContext, cancel := context.WithTimeout(ctx, project.RunTimeLimit)
	defer cancel()
	runLog := &runLogWriter{repository: service.RunLogRepository, project: project, runIdentifier: run.RunIdentifier}

	stepOutcomes := service.runStepsDeclaredByTheCommit(runContext, run, runLog)
	service.cleanUpAfterRun(run, runLog)

	return domain.RunResult{
		ProjectName:   project.Name,
		CommitSha:     run.CommitSha,
		RunIdentifier: run.RunIdentifier,
		Passed:        everyStepPassed(stepOutcomes),
		StepOutcomes:  stepOutcomes,
		FinishedAt:    time.Now(),
	}, runLog.firstWriteError
}

// RecordFinishedRun saves the result first. A crash between the two writes then only tests the commit
// again, where the reverse order could lose a result.
func (service *RunService) RecordFinishedRun(project domain.Project, result domain.RunResult) error {
	if err := service.UnreportedResultRepository.SaveUnreportedResult(result); err != nil {
		return err
	}
	return service.LastTestedCommitRepository.SaveLastTestedCommit(project, result.CommitSha)
}

func (service *RunService) runStepsDeclaredByTheCommit(ctx context.Context, run domain.PreparedRun, runLog *runLogWriter) []domain.StepOutcome {
	steps, err := service.StepDefinitionReader.ReadStepDefinitions(run.StepEnvironment.CheckoutDirectory)
	if err != nil {
		runLog.write("step %s %s: %v", domain.ReadStepDefinitionsStepName, domain.StepFailed, err)
		return []domain.StepOutcome{{StepName: domain.ReadStepDefinitionsStepName, Status: domain.StepFailed}}
	}

	stepOutcomes := make([]domain.StepOutcome, 0, len(steps))
	statusOfEarlierSteps := map[string]domain.StepStatus{}
	runTimeLimitReached := false
	for _, step := range steps {
		var outcome domain.StepOutcome
		dependencyThatDidNotPass, stepIsBlocked := firstDependencyThatDidNotPass(step, statusOfEarlierSteps)
		switch {
		case runTimeLimitReached || ctx.Err() != nil:
			runTimeLimitReached = true
			outcome = skippedStepOutcome(step, "the run time limit was reached", runLog)
		case stepIsBlocked:
			outcome = skippedStepOutcome(step, dependencyThatDidNotPass+" did not pass", runLog)
		default:
			outcome = service.runStepWithItsRepeat(ctx, step, run, runLog)
			runTimeLimitReached = outcome.Status == domain.StepStoppedByRunTimeLimit
		}
		statusOfEarlierSteps[step.Name] = outcome.Status
		stepOutcomes = append(stepOutcomes, outcome)
	}
	return stepOutcomes
}

func firstDependencyThatDidNotPass(step domain.StepDefinition, statusOfEarlierSteps map[string]domain.StepStatus) (string, bool) {
	for _, dependency := range step.DependsOn {
		if !statusOfEarlierSteps[dependency].CountsAsPassed() {
			return dependency, true
		}
	}
	return "", false
}

func skippedStepOutcome(step domain.StepDefinition, reason string, runLog *runLogWriter) domain.StepOutcome {
	runLog.write("step %s %s: %s", step.Name, domain.StepSkipped, reason)
	return domain.StepOutcome{StepName: step.Name, Status: domain.StepSkipped}
}

func (service *RunService) runStepWithItsRepeat(ctx context.Context, step domain.StepDefinition, run domain.PreparedRun, runLog *runLogWriter) domain.StepOutcome {
	startedAt := time.Now()
	runLog.write("step %s started", step.Name)
	err := service.StepProcessRunner.RunStep(ctx, step, run.StepEnvironment)

	repeated := false
	if err != nil && step.RepeatOnceOnFailure && !errors.Is(err, domain.ErrStepStoppedByRunTimeLimit) && ctx.Err() == nil {
		runLog.write("step %s failed, repeating once: %v", step.Name, err)
		repeated = true
		err = service.StepProcessRunner.RunStep(ctx, step, run.StepEnvironment)
	}

	duration := time.Since(startedAt)
	status := statusAfterLastAttempt(err, repeated)
	if err != nil {
		runLog.write("step %s %s after %s: %v", step.Name, status, duration, err)
	} else {
		runLog.write("step %s %s after %s", step.Name, status, duration)
	}
	return domain.StepOutcome{StepName: step.Name, Status: status, Duration: duration}
}

func statusAfterLastAttempt(err error, repeated bool) domain.StepStatus {
	switch {
	case errors.Is(err, domain.ErrStepStoppedByRunTimeLimit):
		return domain.StepStoppedByRunTimeLimit
	case err != nil:
		return domain.StepFailed
	case repeated:
		return domain.StepPassedOnRepeat
	default:
		return domain.StepPassed
	}
}

func (service *RunService) cleanUpAfterRun(run domain.PreparedRun, runLog *runLogWriter) {
	if err := service.LeftoverProcessStopper.StopLeftoverProcesses(); err != nil {
		runLog.write("could not stop leftover processes: %v", err)
	}
	if err := service.TemporaryFolders.DeletePrivateTemporaryFolder(run.StepEnvironment.PrivateTemporaryFolder); err != nil {
		runLog.write("could not delete the private temporary folder: %v", err)
	}
}

func everyStepPassed(stepOutcomes []domain.StepOutcome) bool {
	for _, outcome := range stepOutcomes {
		if !outcome.Status.CountsAsPassed() {
			return false
		}
	}
	return true
}

type runLogWriter struct {
	repository      repository.RunLogRepository
	project         domain.Project
	runIdentifier   string
	firstWriteError error
}

func (writer *runLogWriter) write(format string, arguments ...any) {
	line := "=== " + fmt.Sprintf(format, arguments...) + "\n"
	if err := writer.repository.AppendToRunLog(writer.project, writer.runIdentifier, line); err != nil && writer.firstWriteError == nil {
		writer.firstWriteError = fmt.Errorf("write run log: %w", err)
	}
}
