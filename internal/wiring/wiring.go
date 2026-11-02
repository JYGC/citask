package wiring

import (
	"path/filepath"
	"time"

	"github.com/JYGC/citask/internal/application"
	"github.com/JYGC/citask/internal/config"
	"github.com/JYGC/citask/internal/domain"
	"github.com/JYGC/citask/internal/reportservice"
	"github.com/JYGC/citask/internal/repository"
	"github.com/JYGC/citask/internal/runservice"
	"github.com/JYGC/citask/internal/store"
)

const (
	gitNetworkTimeLimit       = 60 * time.Second
	gitHubRequestTimeLimit    = 30 * time.Second
	heartbeatRequestTimeLimit = 10 * time.Second
	ProcessStopGracePeriod    = 10 * time.Second

	// Chromium's unveil policy allows only /tmp, so a private folder must be inside it. A TMPDIR elsewhere
	// fails with "Failed to create a ProcessSingleton".
	temporaryFoldersParentDirectory = "/tmp"
)

type PollSettings struct {
	CiHomeDirectory                 string
	TemporaryFoldersParentDirectory string
	GitHubApiBaseUrl                string
	GitHubTokenFile                 string
	CommitStatusContext             string
	RunLogRetention                 time.Duration
	GitNetworkTimeLimit             time.Duration
	GitHubRequestTimeLimit          time.Duration
	HeartbeatUrl                    string
	HeartbeatRequestTimeLimit       time.Duration
	ProcessStopGracePeriod          time.Duration
	Projects                        []domain.Project
}

func PollSettingsFromConfiguration(configuration config.Configuration) PollSettings {
	projects := make([]domain.Project, 0, len(configuration.Projects))
	for _, project := range configuration.Projects {
		projects = append(projects, project.DomainProject())
	}
	return PollSettings{
		CiHomeDirectory:                 configuration.CiHomeDirectory,
		TemporaryFoldersParentDirectory: temporaryFoldersParentDirectory,
		GitHubApiBaseUrl:                configuration.GitHubApiBaseUrl,
		GitHubTokenFile:                 configuration.GitHubTokenFile,
		CommitStatusContext:             configuration.CommitStatusContext,
		RunLogRetention:                 configuration.RunLogRetention(),
		GitNetworkTimeLimit:             gitNetworkTimeLimit,
		GitHubRequestTimeLimit:          gitHubRequestTimeLimit,
		HeartbeatUrl:                    configuration.HeartbeatUrl,
		HeartbeatRequestTimeLimit:       heartbeatRequestTimeLimit,
		ProcessStopGracePeriod:          ProcessStopGracePeriod,
		Projects:                        projects,
	}
}

// BuildPollAllProjects takes the stopper as an argument so that tests share every other part of the
// real wiring while cmd/citask/main.go stays the only place that passes in the real stopper.
func BuildPollAllProjects(settings PollSettings, leftoverProcessStopper repository.LeftoverProcessStopper) *application.PollAllProjects {
	stateDirectory := filepath.Join(settings.CiHomeDirectory, "state")
	projectState := store.ProjectStateStore{StateDirectory: stateDirectory}
	runLogs := store.RunLogStore{LogsDirectory: filepath.Join(settings.CiHomeDirectory, "logs")}
	gitRepositories := store.GitRepositoryStore{
		ProjectsDirectory: filepath.Join(settings.CiHomeDirectory, "projects"),
		NetworkTimeLimit:  settings.GitNetworkTimeLimit,
	}
	gitHub := store.GitHubStore{
		ApiBaseUrl:          settings.GitHubApiBaseUrl,
		TokenFile:           settings.GitHubTokenFile,
		CommitStatusContext: settings.CommitStatusContext,
		RequestTimeLimit:    settings.GitHubRequestTimeLimit,
	}

	runService := runservice.New(runservice.Dependencies{
		ExclusivePollLock:          store.ExclusivePollFileLock{LockFile: filepath.Join(stateDirectory, "citask.lock")},
		TrunkCommitReader:          gitRepositories,
		CleanCheckoutPreparer:      gitRepositories,
		StepDefinitionReader:       store.StepDefinitionFileReader{},
		LastTestedCommitRepository: projectState,
		UnreportedResultRepository: projectState,
		RunLogRepository:           runLogs,
		TemporaryFolders:           store.PrivateTemporaryFolderStore{ParentDirectory: settings.TemporaryFoldersParentDirectory},
		StepProcessRunner:          store.StepProcessStore{ProcessStopGracePeriod: settings.ProcessStopGracePeriod},
		LeftoverProcessStopper:     leftoverProcessStopper,
		RunLogRetention:            settings.RunLogRetention,
	})
	reportService := reportservice.New(reportservice.Dependencies{
		CommitStatusPublisher:      gitHub,
		FailureIssueTracker:        gitHub,
		UnreportedResultRepository: projectState,
		RunLogRepository:           runLogs,
		HeartbeatSender:            store.HeartbeatStore{Url: settings.HeartbeatUrl, RequestTimeLimit: settings.HeartbeatRequestTimeLimit},
	})
	return application.NewPollAllProjects(settings.Projects, runService, reportService)
}
