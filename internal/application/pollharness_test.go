package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JYGC/citask/internal/domain"
	"github.com/JYGC/citask/internal/wiring"
)

const (
	fakeGitHubToken         = "fake-token-for-tests-only"
	testGitHubOwner         = "test-owner"
	commitStatusContext     = "ci/citask"
	defaultTestRunTimeLimit = time.Minute
	shortRunTimeLimit       = 2 * time.Second
	shortProcessStopGrace   = 200 * time.Millisecond
	generousBoundForAPoll   = 30 * time.Second
)

type stepFixture struct {
	Name                string
	WorkingDirectory    string
	Command             []string
	Environment         map[string]string `json:",omitempty"`
	DependsOn           []string          `json:",omitempty"`
	RepeatOnceOnFailure bool              `json:",omitempty"`
}

func shellStep(name, script string) stepFixture {
	return stepFixture{Name: name, WorkingDirectory: ".", Command: []string{"sh", "-c", script}}
}

func (step stepFixture) dependingOn(stepNames ...string) stepFixture {
	step.DependsOn = stepNames
	return step
}

func (step stepFixture) repeatingOnceOnFailure() stepFixture {
	step.RepeatOnceOnFailure = true
	return step
}

type recordingLeftoverProcessStopper struct {
	temporaryFoldersParentDirectory     string
	callCount                           int
	privateTemporaryFolderExistedDuring []bool
}

func (stopper *recordingLeftoverProcessStopper) StopLeftoverProcesses() error {
	stopper.callCount++
	privateTemporaryFolders, err := filepath.Glob(filepath.Join(stopper.temporaryFoldersParentDirectory, "citask-*"))
	if err != nil {
		return err
	}
	stopper.privateTemporaryFolderExistedDuring = append(stopper.privateTemporaryFolderExistedDuring, len(privateTemporaryFolders) > 0)
	return nil
}

type pollHarness struct {
	t                               *testing.T
	ciHomeDirectory                 string
	temporaryFoldersParentDirectory string
	markerDirectory                 string
	gitHubTokenFile                 string
	gitHubRequestTimeLimit          time.Duration
	heartbeatUrl                    string
	heartbeatRequestTimeLimit       time.Duration
	github                          *gitHubStub
	leftoverProcessStopper          *recordingLeftoverProcessStopper
	projects                        []*enrolledProject
}

func newPollHarness(t *testing.T) *pollHarness {
	t.Helper()
	temporaryFoldersParentDirectory := t.TempDir()
	gitHubTokenFile := filepath.Join(t.TempDir(), "github-token")
	if err := os.WriteFile(gitHubTokenFile, []byte(fakeGitHubToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &pollHarness{
		t:                               t,
		ciHomeDirectory:                 t.TempDir(),
		temporaryFoldersParentDirectory: temporaryFoldersParentDirectory,
		markerDirectory:                 t.TempDir(),
		gitHubTokenFile:                 gitHubTokenFile,
		gitHubRequestTimeLimit:          30 * time.Second,
		heartbeatRequestTimeLimit:       10 * time.Second,
		github:                          newGitHubStub(t),
		leftoverProcessStopper: &recordingLeftoverProcessStopper{
			temporaryFoldersParentDirectory: temporaryFoldersParentDirectory,
		},
	}
}

func (harness *pollHarness) settings() wiring.PollSettings {
	projects := make([]domain.Project, 0, len(harness.projects))
	for _, project := range harness.projects {
		projects = append(projects, project.definition)
	}
	return wiring.PollSettings{
		CiHomeDirectory:                 harness.ciHomeDirectory,
		TemporaryFoldersParentDirectory: harness.temporaryFoldersParentDirectory,
		GitHubApiBaseUrl:                harness.github.server.URL,
		GitHubTokenFile:                 harness.gitHubTokenFile,
		CommitStatusContext:             commitStatusContext,
		RunLogRetention:                 30 * 24 * time.Hour,
		GitNetworkTimeLimit:             30 * time.Second,
		GitHubRequestTimeLimit:          harness.gitHubRequestTimeLimit,
		HeartbeatUrl:                    harness.heartbeatUrl,
		HeartbeatRequestTimeLimit:       harness.heartbeatRequestTimeLimit,
		ProcessStopGracePeriod:          shortProcessStopGrace,
		Projects:                        projects,
	}
}

func (harness *pollHarness) pollReturningError() error {
	return wiring.BuildPollAllProjects(harness.settings(), harness.leftoverProcessStopper).Execute(context.Background())
}

func (harness *pollHarness) poll() {
	harness.t.Helper()
	if err := harness.pollReturningError(); err != nil {
		harness.t.Fatalf("poll returned an error: %v", err)
	}
}

func (harness *pollHarness) markerPath(markerName string) string {
	return filepath.Join(harness.markerDirectory, markerName)
}

func (harness *pollHarness) scriptRecordingStepRan(markerName string) string {
	return fmt.Sprintf("echo ran >> %q", harness.markerPath(markerName))
}

func (harness *pollHarness) stepRunCount(markerName string) int {
	harness.t.Helper()
	content, err := os.ReadFile(harness.markerPath(markerName))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		harness.t.Fatal(err)
	}
	return strings.Count(string(content), "ran\n")
}

func (harness *pollHarness) markerExists(markerName string) bool {
	_, err := os.Stat(harness.markerPath(markerName))
	return err == nil
}

func (harness *pollHarness) exclusivePollLockFile() string {
	return filepath.Join(harness.ciHomeDirectory, "state", "citask.lock")
}

func (harness *pollHarness) holdExclusivePollLock() (releaseLock func()) {
	harness.t.Helper()
	lockFile := openExclusivePollLockFile(harness.t, harness.exclusivePollLockFile())
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		harness.t.Fatalf("hold lock: %v", err)
	}
	return func() { lockFile.Close() }
}

func openExclusivePollLockFile(t *testing.T, path string) *os.File {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	lockFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	return lockFile
}

type enrolledProject struct {
	harness                 *pollHarness
	definition              domain.Project
	bareRepositoryDirectory string
	authoringDirectory      string
}

func (harness *pollHarness) enrolProject(name, trunkBranch string) *enrolledProject {
	harness.t.Helper()
	bareRepositoryDirectory := filepath.Join(harness.t.TempDir(), name+".git")
	authoringDirectory := harness.t.TempDir()
	runGit(harness.t, harness.t.TempDir(), "init", "--quiet", "--bare", "--initial-branch="+trunkBranch, bareRepositoryDirectory)
	runGit(harness.t, authoringDirectory, "init", "--quiet", "--initial-branch="+trunkBranch)
	runGit(harness.t, authoringDirectory, "remote", "add", "origin", bareRepositoryDirectory)
	project := &enrolledProject{
		harness: harness,
		definition: domain.Project{
			Name:             name,
			CloneUrl:         bareRepositoryDirectory,
			GitHubOwner:      testGitHubOwner,
			GitHubRepository: name,
			TrunkBranch:      trunkBranch,
			RunTimeLimit:     defaultTestRunTimeLimit,
		},
		bareRepositoryDirectory: bareRepositoryDirectory,
		authoringDirectory:      authoringDirectory,
	}
	harness.projects = append(harness.projects, project)
	return project
}

func runGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Test Author",
		"GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Test Author",
		"GIT_COMMITTER_EMAIL=author@example.com",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func (project *enrolledProject) commitFilesOnBranch(branch string, files map[string]string) string {
	t := project.harness.t
	t.Helper()
	runGit(t, project.authoringDirectory, "checkout", "--quiet", "-B", branch)
	for relativePath, content := range files {
		absolutePath := filepath.Join(project.authoringDirectory, relativePath)
		if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolutePath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, project.authoringDirectory, "add", "--all")
	runGit(t, project.authoringDirectory, "commit", "--quiet", "--allow-empty", "--message", "test commit")
	runGit(t, project.authoringDirectory, "push", "--quiet", "--force", "origin", branch)
	return runGit(t, project.authoringDirectory, "rev-parse", "HEAD")
}

func stepDefinitionFileContent(t *testing.T, steps ...stepFixture) string {
	t.Helper()
	content, err := json.Marshal(struct{ Steps []stepFixture }{Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func (project *enrolledProject) commitStepsOnTrunk(steps ...stepFixture) string {
	project.harness.t.Helper()
	return project.commitFilesOnBranch(project.definition.TrunkBranch, map[string]string{
		"ci-steps.json": stepDefinitionFileContent(project.harness.t, steps...),
	})
}

func (project *enrolledProject) commitPassingStepOnTrunk() string {
	project.harness.t.Helper()
	return project.commitStepsOnTrunk(shellStep("passing-step", "exit 0"))
}

func (project *enrolledProject) commitFailingStepOnTrunk() string {
	project.harness.t.Helper()
	return project.commitStepsOnTrunk(shellStep("failing-step", "exit 1"))
}

func (project *enrolledProject) failureIssueTitle() string {
	return "CI failing on " + project.definition.TrunkBranch + " (citask)"
}

func (project *enrolledProject) lastTestedCommit() string {
	project.harness.t.Helper()
	content, err := os.ReadFile(filepath.Join(project.stateDirectory(), "last-tested-commit"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		project.harness.t.Fatal(err)
	}
	return strings.TrimSpace(string(content))
}

func (project *enrolledProject) stateDirectory() string {
	return filepath.Join(project.harness.ciHomeDirectory, "state", project.definition.Name)
}

func (project *enrolledProject) logsDirectory() string {
	return filepath.Join(project.harness.ciHomeDirectory, "logs", project.definition.Name)
}

func (project *enrolledProject) cloneDirectory() string {
	return filepath.Join(project.harness.ciHomeDirectory, "projects", project.definition.Name)
}

func (project *enrolledProject) runIdentifiers() []string {
	project.harness.t.Helper()
	runLogFiles, err := filepath.Glob(filepath.Join(project.logsDirectory(), "*", "run.log"))
	if err != nil {
		project.harness.t.Fatal(err)
	}
	identifiers := make([]string, 0, len(runLogFiles))
	for _, runLogFile := range runLogFiles {
		identifiers = append(identifiers, filepath.Base(filepath.Dir(runLogFile)))
	}
	sort.Strings(identifiers)
	return identifiers
}

func (project *enrolledProject) textOfAllRunLogs() string {
	project.harness.t.Helper()
	var everything strings.Builder
	for _, runIdentifier := range project.runIdentifiers() {
		content, err := os.ReadFile(filepath.Join(project.logsDirectory(), runIdentifier, "run.log"))
		if err != nil {
			project.harness.t.Fatal(err)
		}
		everything.Write(content)
	}
	return everything.String()
}

func (project *enrolledProject) textOfPollProblemsLog() string {
	project.harness.t.Helper()
	content, err := os.ReadFile(filepath.Join(project.logsDirectory(), "poll-problems.log"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		project.harness.t.Fatal(err)
	}
	return string(content)
}

func (project *enrolledProject) textOfArtifactFromLatestRun(artifactName string) string {
	t := project.harness.t
	t.Helper()
	runIdentifiers := project.runIdentifiers()
	if len(runIdentifiers) == 0 {
		t.Fatal("no run has written a log")
	}
	latestRunIdentifier := runIdentifiers[len(runIdentifiers)-1]
	content, err := os.ReadFile(filepath.Join(project.logsDirectory(), latestRunIdentifier, "artifacts", artifactName))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(content))
}

func (project *enrolledProject) unreportedResultCount() int {
	project.harness.t.Helper()
	resultFiles, err := filepath.Glob(filepath.Join(project.stateDirectory(), "unreported-results", "*.json"))
	if err != nil {
		project.harness.t.Fatal(err)
	}
	return len(resultFiles)
}

func (project *enrolledProject) commitStatuses() []recordedCommitStatus {
	return project.harness.github.commitStatuses(testGitHubOwner, project.definition.GitHubRepository)
}

func (project *enrolledProject) commitStatusStatesFor(commitSha string) []string {
	states := []string{}
	for _, status := range project.commitStatuses() {
		if status.CommitSha == commitSha {
			states = append(states, status.State)
		}
	}
	return states
}

func (project *enrolledProject) failureIssues() []stubIssue {
	return project.harness.github.issuesExcludingPullRequests(testGitHubOwner, project.definition.GitHubRepository)
}

func (project *enrolledProject) onlyFailureIssue() stubIssue {
	t := project.harness.t
	t.Helper()
	issues := project.failureIssues()
	if len(issues) != 1 {
		t.Fatalf("expected exactly one failure issue on %s, got %d: %+v", project.definition.Name, len(issues), issues)
	}
	return issues[0]
}

func assertEqual[Value comparable](t *testing.T, description string, got, want Value) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", description, got, want)
	}
}

func assertStringSlicesEqual(t *testing.T, description string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("%s: got %v, want %v", description, got, want)
	}
}

func assertContains(t *testing.T, description, text, expectedSubstring string) {
	t.Helper()
	if !strings.Contains(text, expectedSubstring) {
		t.Errorf("%s: expected %q in:\n%s", description, expectedSubstring, text)
	}
}

func assertDoesNotContain(t *testing.T, description, text, forbiddenSubstring string) {
	t.Helper()
	if strings.Contains(text, forbiddenSubstring) {
		t.Errorf("%s: did not expect %q in:\n%s", description, forbiddenSubstring, text)
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func assertPathIsGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected %s to be gone, stat returned: %v", path, err)
	}
}
