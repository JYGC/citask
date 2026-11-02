package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

const (
	maximumMinutes = int(math.MaxInt64 / int64(time.Minute))
	maximumDays    = int(math.MaxInt64 / int64(24*time.Hour))
)

var (
	projectNameIsSafeAsAFolderName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	trunkBranchCannotBeAnOption    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	gitHubNameIsSafeInAPath        = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

type ProjectConfiguration struct {
	Name                string
	CloneUrl            string
	GitHubOwner         string
	GitHubRepository    string
	TrunkBranch         string
	RunTimeLimitMinutes int
}

func (project ProjectConfiguration) DomainProject() domain.Project {
	return domain.Project{
		Name:             project.Name,
		CloneUrl:         project.CloneUrl,
		GitHubOwner:      project.GitHubOwner,
		GitHubRepository: project.GitHubRepository,
		TrunkBranch:      project.TrunkBranch,
		RunTimeLimit:     time.Duration(project.RunTimeLimitMinutes) * time.Minute,
	}
}

type Configuration struct {
	CiHomeDirectory     string
	GitHubApiBaseUrl    string
	GitHubTokenFile     string
	CommitStatusContext string
	RunLogRetentionDays int
	Projects            []ProjectConfiguration
}

func (configuration Configuration) RunLogRetention() time.Duration {
	return time.Duration(configuration.RunLogRetentionDays) * 24 * time.Hour
}

// Load reads citask.json from the directory containing the executable.
func Load() (Configuration, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return Configuration{}, fmt.Errorf("resolve executable path: %w", err)
	}
	return loadFromFile(filepath.Join(filepath.Dir(executablePath), "citask.json"))
}

func loadFromFile(configurationFilePath string) (Configuration, error) {
	configurationFile, err := os.Open(configurationFilePath)
	if err != nil {
		return Configuration{}, err
	}
	defer configurationFile.Close()

	var configuration Configuration
	decoder := json.NewDecoder(configurationFile)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return Configuration{}, fmt.Errorf("decode %s: %w", configurationFilePath, err)
	}
	if err := configuration.validate(); err != nil {
		return Configuration{}, err
	}
	return configuration, nil
}

// validate reports every failing field at once, because citask.json is edited by hand with doas and a
// one-error-per-attempt cycle is slow to work through.
func (configuration Configuration) validate() error {
	var validationErrors []error
	addError := func(format string, arguments ...any) {
		validationErrors = append(validationErrors, fmt.Errorf(format, arguments...))
	}

	if !filepath.IsAbs(configuration.CiHomeDirectory) {
		addError("CiHomeDirectory must be an absolute path, got %q", configuration.CiHomeDirectory)
	}
	if !filepath.IsAbs(configuration.GitHubTokenFile) {
		addError("GitHubTokenFile must be an absolute path, got %q", configuration.GitHubTokenFile)
	}
	if err := validateHttpUrl("GitHubApiBaseUrl", configuration.GitHubApiBaseUrl); err != nil {
		validationErrors = append(validationErrors, err)
	}
	if configuration.CommitStatusContext == "" {
		addError("CommitStatusContext must not be empty")
	}
	if configuration.RunLogRetentionDays < 1 || configuration.RunLogRetentionDays > maximumDays {
		addError("RunLogRetentionDays must be between 1 and %d, got %d", maximumDays, configuration.RunLogRetentionDays)
	}
	if len(configuration.Projects) == 0 {
		addError("Projects must list at least one project")
	}

	projectNamesAlreadySeen := map[string]bool{}
	for projectIndex, project := range configuration.Projects {
		projectLabel := fmt.Sprintf("project %d (%q)", projectIndex+1, project.Name)
		if projectNamesAlreadySeen[project.Name] {
			addError("%s has a duplicate Name", projectLabel)
		}
		projectNamesAlreadySeen[project.Name] = true
		validationErrors = append(validationErrors, project.validate(projectLabel)...)
	}
	return errors.Join(validationErrors...)
}

func (project ProjectConfiguration) validate(projectLabel string) []error {
	var validationErrors []error
	addError := func(format string, arguments ...any) {
		validationErrors = append(validationErrors, fmt.Errorf("%s %s", projectLabel, fmt.Sprintf(format, arguments...)))
	}

	if !projectNameIsSafeAsAFolderName.MatchString(project.Name) {
		addError("must have a Name matching %s, because it becomes a folder name", projectNameIsSafeAsAFolderName)
	}
	if project.CloneUrl == "" || project.CloneUrl[0] == '-' {
		addError("must have a CloneUrl that does not start with a dash, got %q", project.CloneUrl)
	}
	if !gitHubNameIsSafeInAPath.MatchString(project.GitHubOwner) {
		addError("must have a GitHubOwner matching %s, got %q", gitHubNameIsSafeInAPath, project.GitHubOwner)
	}
	if !gitHubNameIsSafeInAPath.MatchString(project.GitHubRepository) {
		addError("must have a GitHubRepository matching %s, got %q", gitHubNameIsSafeInAPath, project.GitHubRepository)
	}
	if !trunkBranchCannotBeAnOption.MatchString(project.TrunkBranch) {
		addError("must have a TrunkBranch matching %s, got %q", trunkBranchCannotBeAnOption, project.TrunkBranch)
	}
	if project.RunTimeLimitMinutes < 1 || project.RunTimeLimitMinutes > maximumMinutes {
		addError("must have a RunTimeLimitMinutes between 1 and %d, got %d", maximumMinutes, project.RunTimeLimitMinutes)
	}
	return validationErrors
}

func validateHttpUrl(fieldName, value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("%s must be a valid URL, got %q: %w", fieldName, value, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must have an http or https scheme, got %q", fieldName, value)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s must have a non-empty host, got %q", fieldName, value)
	}
	return nil
}
