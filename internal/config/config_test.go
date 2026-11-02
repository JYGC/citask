package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validConfigurationJson = `{
  "CiHomeDirectory": "/var/ci",
  "GitHubApiBaseUrl": "https://api.github.example",
  "GitHubTokenFile": "/var/ci/etc/github-token",
  "CommitStatusContext": "ci/citask",
  "RunLogRetentionDays": 30,
  "Projects": [
    {
      "Name": "example-project",
      "CloneUrl": "https://github.example/example-owner/example-repository",
      "GitHubOwner": "example-owner",
      "GitHubRepository": "example-repository",
      "TrunkBranch": "master",
      "RunTimeLimitMinutes": 30
    }
  ]
}`

func validConfiguration() Configuration {
	return Configuration{
		CiHomeDirectory:     "/var/ci",
		GitHubApiBaseUrl:    "https://api.github.example",
		GitHubTokenFile:     "/var/ci/etc/github-token",
		CommitStatusContext: "ci/citask",
		RunLogRetentionDays: 30,
		Projects:            []ProjectConfiguration{validProjectConfiguration("example-project")},
	}
}

func validProjectConfiguration(name string) ProjectConfiguration {
	return ProjectConfiguration{
		Name:                name,
		CloneUrl:            "https://github.example/example-owner/example-repository",
		GitHubOwner:         "example-owner",
		GitHubRepository:    "example-repository",
		TrunkBranch:         "master",
		RunTimeLimitMinutes: 30,
	}
}

func writeConfigurationFile(t *testing.T, content string) string {
	t.Helper()
	configurationFile := filepath.Join(t.TempDir(), "citask.json")
	if err := os.WriteFile(configurationFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return configurationFile
}

func TestLoadFromFileAcceptsAValidConfiguration(t *testing.T) {
	configuration, err := LoadFromFile(writeConfigurationFile(t, validConfigurationJson))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Projects[0].Name != "example-project" {
		t.Errorf("project name: got %q", configuration.Projects[0].Name)
	}
	if configuration.CommitStatusContext != "ci/citask" {
		t.Errorf("commit status context: got %q", configuration.CommitStatusContext)
	}
}

func TestTheExampleConfigurationInTheRepoIsValid(t *testing.T) {
	if _, err := LoadFromFile(filepath.Join("..", "..", "citask.example.json")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFromFileReportsAMissingFile(t *testing.T) {
	if _, err := LoadFromFile(filepath.Join(t.TempDir(), "citask.json")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestLoadFromFileRejectsAnUnknownField(t *testing.T) {
	misspeltField := strings.Replace(validConfigurationJson, `"TrunkBranch"`, `"TrunkBranchh"`, 1)
	_, err := LoadFromFile(writeConfigurationFile(t, misspeltField))
	if err == nil || !strings.Contains(err.Error(), "TrunkBranchh") {
		t.Fatalf("expected the unknown field to be named, got %v", err)
	}
}

func TestValidationNamesEveryFailingFieldAtOnce(t *testing.T) {
	configuration := Configuration{
		Projects: []ProjectConfiguration{{Name: "example-project"}},
	}
	err := configuration.validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, expectedFieldName := range []string{
		"CiHomeDirectory", "GitHubApiBaseUrl", "GitHubTokenFile", "CommitStatusContext", "RunLogRetentionDays",
		"CloneUrl", "GitHubOwner", "GitHubRepository", "TrunkBranch", "RunTimeLimitMinutes",
	} {
		if !strings.Contains(err.Error(), expectedFieldName) {
			t.Errorf("expected %s to be named in: %v", expectedFieldName, err)
		}
	}
}

func TestValidationQuotesAProjectNameContainingAPercentSign(t *testing.T) {
	configuration := validConfiguration()
	configuration.Projects[0].Name = "100%d-broken"
	configuration.Projects[0].GitHubOwner = ""
	err := configuration.validate()
	if err == nil || !strings.Contains(err.Error(), "100%d-broken") {
		t.Fatalf("expected the name to appear unchanged, got %v", err)
	}
}

func TestValidationRejectsAConfigurationWithNoProjects(t *testing.T) {
	configuration := validConfiguration()
	configuration.Projects = nil
	if err := configuration.validate(); err == nil || !strings.Contains(err.Error(), "Projects") {
		t.Fatalf("expected Projects to be named, got %v", err)
	}
}

func TestValidationRejectsDuplicateProjectNames(t *testing.T) {
	configuration := validConfiguration()
	configuration.Projects = append(configuration.Projects, validProjectConfiguration("example-project"))
	if err := configuration.validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected a duplicate name error, got %v", err)
	}
}

func TestValidationRejectsProjectNamesThatAreUnsafeAsFolderNames(t *testing.T) {
	for _, unsafeName := range []string{"", ".", "..", "../outside", "folder/name", ".hidden", "-option", "has space", "new\nline"} {
		t.Run(unsafeName, func(t *testing.T) {
			configuration := validConfiguration()
			configuration.Projects[0].Name = unsafeName
			if err := configuration.validate(); err == nil || !strings.Contains(err.Error(), "Name") {
				t.Fatalf("expected the name to be rejected, got %v", err)
			}
		})
	}
}

func TestValidationAcceptsProjectNamesThatAreSafeAsFolderNames(t *testing.T) {
	for _, safeName := range []string{"citask", "Project2", "with-dash", "with_underscore", "with.dot"} {
		configuration := validConfiguration()
		configuration.Projects[0].Name = safeName
		if err := configuration.validate(); err != nil {
			t.Errorf("%q should be accepted: %v", safeName, err)
		}
	}
}

func TestValidationRejectsValuesThatGitWouldReadAsOptions(t *testing.T) {
	configuration := validConfiguration()
	configuration.Projects[0].CloneUrl = "--upload-pack=touch /tmp/oops"
	configuration.Projects[0].TrunkBranch = "--output=/tmp/oops"
	err := configuration.validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, expectedFieldName := range []string{"CloneUrl", "TrunkBranch"} {
		if !strings.Contains(err.Error(), expectedFieldName) {
			t.Errorf("expected %s to be named in: %v", expectedFieldName, err)
		}
	}
}

func TestValidationAcceptsATrunkBranchWithASlash(t *testing.T) {
	configuration := validConfiguration()
	configuration.Projects[0].TrunkBranch = "release/stable"
	if err := configuration.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidationRejectsGitHubNamesThatWouldChangeTheRequestPath(t *testing.T) {
	configuration := validConfiguration()
	configuration.Projects[0].GitHubOwner = "owner/../other"
	configuration.Projects[0].GitHubRepository = "repository?query"
	err := configuration.validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, expectedFieldName := range []string{"GitHubOwner", "GitHubRepository"} {
		if !strings.Contains(err.Error(), expectedFieldName) {
			t.Errorf("expected %s to be named in: %v", expectedFieldName, err)
		}
	}
}

func TestValidationRejectsRelativePaths(t *testing.T) {
	configuration := validConfiguration()
	configuration.CiHomeDirectory = "var/ci"
	configuration.GitHubTokenFile = "github-token"
	err := configuration.validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, expectedFieldName := range []string{"CiHomeDirectory", "GitHubTokenFile"} {
		if !strings.Contains(err.Error(), expectedFieldName) {
			t.Errorf("expected %s to be named in: %v", expectedFieldName, err)
		}
	}
}

func TestValidationRejectsAGitHubApiBaseUrlThatIsNotHttp(t *testing.T) {
	for _, invalidUrl := range []string{"ftp://api.github.example", "not a url", "https://"} {
		configuration := validConfiguration()
		configuration.GitHubApiBaseUrl = invalidUrl
		if err := configuration.validate(); err == nil || !strings.Contains(err.Error(), "GitHubApiBaseUrl") {
			t.Errorf("%q should be rejected, got %v", invalidUrl, err)
		}
	}
}

func TestValidationRejectsLimitsThatWouldOverflowADuration(t *testing.T) {
	configuration := validConfiguration()
	configuration.RunLogRetentionDays = maximumDays + 1
	configuration.Projects[0].RunTimeLimitMinutes = maximumMinutes + 1
	err := configuration.validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, expectedFieldName := range []string{"RunLogRetentionDays", "RunTimeLimitMinutes"} {
		if !strings.Contains(err.Error(), expectedFieldName) {
			t.Errorf("expected %s to be named in: %v", expectedFieldName, err)
		}
	}
}

func TestDomainProjectCarriesTheTimeLimitAsADuration(t *testing.T) {
	project := validProjectConfiguration("example-project").DomainProject()
	if project.Name != "example-project" || project.TrunkBranch != "master" {
		t.Errorf("project fields were not carried over: %+v", project)
	}
	if project.RunTimeLimit != 30*time.Minute {
		t.Errorf("run time limit: got %s", project.RunTimeLimit)
	}
}

func TestRunLogRetentionIsADuration(t *testing.T) {
	if retention := validConfiguration().RunLogRetention(); retention != 30*24*time.Hour {
		t.Errorf("run log retention: got %s", retention)
	}
}
