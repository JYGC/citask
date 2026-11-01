package domain

import (
	"errors"
	"time"
)

type Project struct {
	Name             string
	CloneUrl         string
	GitHubOwner      string
	GitHubRepository string
	TrunkBranch      string
	RunTimeLimit     time.Duration
}

type CommitStatusState string

const (
	CommitStatusPending CommitStatusState = "pending"
	CommitStatusSuccess CommitStatusState = "success"
	CommitStatusFailure CommitStatusState = "failure"
)

type CommitStatus struct {
	State       CommitStatusState
	Description string
}

type StepDefinition struct {
	Name                string
	WorkingDirectory    string
	Command             []string
	Environment         map[string]string
	DependsOn           []string
	RepeatOnceOnFailure bool
}

type StepStatus string

const (
	StepPassed                StepStatus = "passed"
	StepPassedOnRepeat        StepStatus = "passed-on-repeat"
	StepFailed                StepStatus = "failed"
	StepSkipped               StepStatus = "skipped"
	StepStoppedByRunTimeLimit StepStatus = "stopped-by-run-time-limit"
)

func (status StepStatus) CountsAsPassed() bool {
	return status == StepPassed || status == StepPassedOnRepeat
}

type StepOutcome struct {
	StepName string
	Status   StepStatus
	Duration time.Duration
}

// RunResult is what gets reported, so it deliberately holds an identifier rather than a log path. A
// path cannot reach GitHub by accident if the reporting code never has one.
type RunResult struct {
	ProjectName   string
	CommitSha     string
	RunIdentifier string
	Passed        bool
	StepOutcomes  []StepOutcome
	FinishedAt    time.Time
}

type StepEnvironment struct {
	CheckoutDirectory      string
	PrivateTemporaryFolder string
	RunLogFile             string
	RunArtifactDirectory   string
}

type PreparedRun struct {
	CommitSha       string
	RunIdentifier   string
	StepEnvironment StepEnvironment
}

type RunLogLocation struct {
	RunLogFile           string
	RunArtifactDirectory string
}

var ErrStepStoppedByRunTimeLimit = errors.New("step stopped by the run time limit")
