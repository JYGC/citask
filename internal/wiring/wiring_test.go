package wiring

import (
	"testing"
	"time"

	"github.com/JYGC/citask/internal/config"
)

func TestSettingsFromConfigurationUseTheAgreedLimits(t *testing.T) {
	settings := PollSettingsFromConfiguration(config.Configuration{
		CiHomeDirectory:     "/var/ci",
		GitHubApiBaseUrl:    "https://api.github.example",
		GitHubTokenFile:     "/var/ci/etc/github-token",
		CommitStatusContext: "ci/citask",
		RunLogRetentionDays: 30,
		Projects: []config.ProjectConfiguration{
			{Name: "first-project", TrunkBranch: "master", RunTimeLimitMinutes: 30},
			{Name: "second-project", TrunkBranch: "develop", RunTimeLimitMinutes: 10},
		},
	})

	if settings.GitNetworkTimeLimit != 60*time.Second {
		t.Errorf("git network time limit: %s", settings.GitNetworkTimeLimit)
	}
	if settings.GitHubRequestTimeLimit != 30*time.Second {
		t.Errorf("GitHub request time limit: %s", settings.GitHubRequestTimeLimit)
	}
	if settings.ProcessStopGracePeriod != 10*time.Second {
		t.Errorf("process stop grace period: %s", settings.ProcessStopGracePeriod)
	}
	if settings.TemporaryFoldersParentDirectory != "/tmp" {
		t.Errorf("temporary folders parent: %s", settings.TemporaryFoldersParentDirectory)
	}
	if settings.RunLogRetention != 30*24*time.Hour {
		t.Errorf("run log retention: %s", settings.RunLogRetention)
	}
	if len(settings.Projects) != 2 || settings.Projects[1].RunTimeLimit != 10*time.Minute || settings.Projects[1].TrunkBranch != "develop" {
		t.Errorf("projects: %+v", settings.Projects)
	}
}
