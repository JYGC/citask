package repository

import (
	"context"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

type ExclusivePollLock interface {
	TryAcquireExclusivePollLock() (releaseLock func() error, acquired bool, err error)
}

type TrunkCommitReader interface {
	ReadCurrentTrunkCommit(ctx context.Context, project domain.Project) (commitSha string, err error)
}

type CleanCheckoutPreparer interface {
	PrepareCleanCheckoutAtCommit(ctx context.Context, project domain.Project, commitSha string) (checkoutDirectory string, err error)
}

type StepDefinitionReader interface {
	ReadStepDefinitions(checkoutDirectory string) ([]domain.StepDefinition, error)
}

type LastTestedCommitRepository interface {
	ReadLastTestedCommit(project domain.Project) (commitSha string, found bool, err error)
	SaveLastTestedCommit(project domain.Project, commitSha string) error
}

type UnreportedResultRepository interface {
	SaveUnreportedResult(result domain.RunResult) error
	ListUnreportedResultsOldestFirst(project domain.Project) ([]domain.RunResult, error)
	DeleteUnreportedResult(result domain.RunResult) error
}

type RunLogRepository interface {
	CreateRunLog(project domain.Project, runIdentifier string) (domain.RunLogLocation, error)
	AppendToRunLog(project domain.Project, runIdentifier string, text string) error
	RecordPollProblem(project domain.Project, problem string) error
	DeleteRunLogsOlderThan(project domain.Project, cutoff time.Time) error
}

type PrivateTemporaryFolderRepository interface {
	CreatePrivateTemporaryFolder(project domain.Project, commitSha string) (folder string, err error)
	DeletePrivateTemporaryFolder(folder string) error
}

type StepProcessRunner interface {
	RunStep(ctx context.Context, step domain.StepDefinition, environment domain.StepEnvironment) error
}

type LeftoverProcessStopper interface {
	StopLeftoverProcesses() error
}

type HeartbeatSender interface {
	SendHeartbeat(ctx context.Context) error
}

type CommitStatusPublisher interface {
	PublishCommitStatus(ctx context.Context, project domain.Project, commitSha string, status domain.CommitStatus) error
}

type FailureIssueTracker interface {
	FindOpenFailureIssue(ctx context.Context, project domain.Project) (issueNumber int, found bool, err error)
	OpenFailureIssue(ctx context.Context, project domain.Project, body string) error
	CommentOnFailureIssue(ctx context.Context, project domain.Project, issueNumber int, body string) error
	CloseFailureIssue(ctx context.Context, project domain.Project, issueNumber int, closingComment string) error
}
