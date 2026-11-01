package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

const generousGitNetworkTimeLimit = 30 * time.Second

type gitTestRepository struct {
	t                       *testing.T
	bareRepositoryDirectory string
	authoringDirectory      string
}

func runGitForTest(t *testing.T, directory string, arguments ...string) string {
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

func newGitTestRepository(t *testing.T) *gitTestRepository {
	t.Helper()
	repository := &gitTestRepository{
		t:                       t,
		bareRepositoryDirectory: filepath.Join(t.TempDir(), "origin.git"),
		authoringDirectory:      t.TempDir(),
	}
	runGitForTest(t, t.TempDir(), "init", "--quiet", "--bare", "--initial-branch=master", repository.bareRepositoryDirectory)
	runGitForTest(t, repository.authoringDirectory, "init", "--quiet", "--initial-branch=master")
	runGitForTest(t, repository.authoringDirectory, "remote", "add", "origin", repository.bareRepositoryDirectory)
	return repository
}

func (repository *gitTestRepository) commitOnBranch(branch string, files map[string]string) string {
	repository.t.Helper()
	runGitForTest(repository.t, repository.authoringDirectory, "checkout", "--quiet", "-B", branch)
	for relativePath, content := range files {
		if err := os.WriteFile(filepath.Join(repository.authoringDirectory, relativePath), []byte(content), 0o644); err != nil {
			repository.t.Fatal(err)
		}
	}
	runGitForTest(repository.t, repository.authoringDirectory, "add", "--all")
	runGitForTest(repository.t, repository.authoringDirectory, "commit", "--quiet", "--allow-empty", "--message", "test commit")
	runGitForTest(repository.t, repository.authoringDirectory, "push", "--quiet", "--force", "origin", branch)
	return runGitForTest(repository.t, repository.authoringDirectory, "rev-parse", "HEAD")
}

func (repository *gitTestRepository) project(trunkBranch string) domain.Project {
	return domain.Project{Name: "example-project", CloneUrl: repository.bareRepositoryDirectory, TrunkBranch: trunkBranch}
}

func newGitRepositoryStore(t *testing.T) GitRepositoryStore {
	t.Helper()
	return GitRepositoryStore{ProjectsDirectory: t.TempDir(), NetworkTimeLimit: generousGitNetworkTimeLimit}
}

func TestReadingTheTrunkCommitReturnsTheNewestCommitOnTheTrunk(t *testing.T) {
	repository := newGitTestRepository(t)
	repository.commitOnBranch("master", nil)
	newestCommit := repository.commitOnBranch("master", nil)

	commitSha, err := newGitRepositoryStore(t).ReadCurrentTrunkCommit(context.Background(), repository.project("master"))

	if err != nil {
		t.Fatal(err)
	}
	if commitSha != newestCommit {
		t.Errorf("got %s, want %s", commitSha, newestCommit)
	}
}

func TestReadingTheTrunkCommitIgnoresOtherBranchesIncludingOnesEndingInTheTrunkName(t *testing.T) {
	repository := newGitTestRepository(t)
	trunkCommit := repository.commitOnBranch("master", nil)
	repository.commitOnBranch("feature/master", nil)
	repository.commitOnBranch("other-branch", nil)

	commitSha, err := newGitRepositoryStore(t).ReadCurrentTrunkCommit(context.Background(), repository.project("master"))

	if err != nil {
		t.Fatal(err)
	}
	if commitSha != trunkCommit {
		t.Errorf("got %s, want %s", commitSha, trunkCommit)
	}
}

func TestReadingTheTrunkCommitFailsWhenTheBranchDoesNotExist(t *testing.T) {
	repository := newGitTestRepository(t)
	repository.commitOnBranch("master", nil)

	_, err := newGitRepositoryStore(t).ReadCurrentTrunkCommit(context.Background(), repository.project("branch-that-does-not-exist"))

	if err == nil {
		t.Fatal("expected an error for a missing trunk branch")
	}
}

func TestReadingTheTrunkCommitFailsWhenTheRemoteFails(t *testing.T) {
	project := domain.Project{Name: "example-project", CloneUrl: filepath.Join(t.TempDir(), "missing.git"), TrunkBranch: "master"}

	if _, err := newGitRepositoryStore(t).ReadCurrentTrunkCommit(context.Background(), project); err == nil {
		t.Fatal("expected an error for a failing remote")
	}
}

func TestCheckoutIsAtTheRequestedCommitEvenWhenTheTrunkHasMovedOn(t *testing.T) {
	repository := newGitTestRepository(t)
	requestedCommit := repository.commitOnBranch("master", map[string]string{"file.txt": "requested"})
	repository.commitOnBranch("master", map[string]string{"file.txt": "newer"})

	checkoutDirectory, err := newGitRepositoryStore(t).PrepareCleanCheckoutAtCommit(context.Background(), repository.project("master"), requestedCommit)

	if err != nil {
		t.Fatal(err)
	}
	if head := runGitForTest(t, checkoutDirectory, "rev-parse", "HEAD"); head != requestedCommit {
		t.Errorf("HEAD is %s, want %s", head, requestedCommit)
	}
	content, err := os.ReadFile(filepath.Join(checkoutDirectory, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "requested" {
		t.Errorf("file content: %q", content)
	}
}

func TestCheckoutIsCleanAfterAPreviousRunLeftFilesBehind(t *testing.T) {
	repository := newGitTestRepository(t)
	firstCommit := repository.commitOnBranch("master", map[string]string{".gitignore": "*.ignored\n", "tracked.txt": "original"})
	secondCommit := repository.commitOnBranch("master", map[string]string{"second.txt": "second"})
	gitStore := newGitRepositoryStore(t)
	checkoutDirectory, err := gitStore.PrepareCleanCheckoutAtCommit(context.Background(), repository.project("master"), firstCommit)
	if err != nil {
		t.Fatal(err)
	}
	for relativePath, content := range map[string]string{"untracked.txt": "left", "output.ignored": "left", "tracked.txt": "changed by a step"} {
		if err := os.WriteFile(filepath.Join(checkoutDirectory, relativePath), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nestedRepository := filepath.Join(checkoutDirectory, "nested-repository")
	if err := os.Mkdir(nestedRepository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, nestedRepository, "init", "--quiet")

	checkoutDirectory, err = gitStore.PrepareCleanCheckoutAtCommit(context.Background(), repository.project("master"), secondCommit)

	if err != nil {
		t.Fatal(err)
	}
	for _, leftoverName := range []string{"untracked.txt", "output.ignored", "nested-repository"} {
		if _, err := os.Stat(filepath.Join(checkoutDirectory, leftoverName)); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", leftoverName, err)
		}
	}
	content, err := os.ReadFile(filepath.Join(checkoutDirectory, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Errorf("a changed tracked file was not restored: %q", content)
	}
}

func TestCheckoutReusesTheProjectsCloneAndFollowsAChangedCloneUrl(t *testing.T) {
	firstRepository := newGitTestRepository(t)
	firstRepository.commitOnBranch("master", nil)
	secondRepository := newGitTestRepository(t)
	secondCommit := secondRepository.commitOnBranch("master", map[string]string{"moved.txt": "moved"})
	gitStore := newGitRepositoryStore(t)
	firstCommit := runGitForTest(t, firstRepository.authoringDirectory, "rev-parse", "HEAD")
	if _, err := gitStore.PrepareCleanCheckoutAtCommit(context.Background(), firstRepository.project("master"), firstCommit); err != nil {
		t.Fatal(err)
	}

	checkoutDirectory, err := gitStore.PrepareCleanCheckoutAtCommit(context.Background(), secondRepository.project("master"), secondCommit)

	if err != nil {
		t.Fatal(err)
	}
	if head := runGitForTest(t, checkoutDirectory, "rev-parse", "HEAD"); head != secondCommit {
		t.Errorf("HEAD is %s, want %s", head, secondCommit)
	}
}

func TestCheckoutFailsWhenTheRemoteFails(t *testing.T) {
	project := domain.Project{Name: "example-project", CloneUrl: filepath.Join(t.TempDir(), "missing.git"), TrunkBranch: "master"}

	if _, err := newGitRepositoryStore(t).PrepareCleanCheckoutAtCommit(context.Background(), project, strings.Repeat("a", 40)); err == nil {
		t.Fatal("expected an error for a failing remote")
	}
}

func TestCheckoutFailsWhenTheCommitIsNotOnTheRemote(t *testing.T) {
	repository := newGitTestRepository(t)
	repository.commitOnBranch("master", nil)

	if _, err := newGitRepositoryStore(t).PrepareCleanCheckoutAtCommit(context.Background(), repository.project("master"), strings.Repeat("a", 40)); err == nil {
		t.Fatal("expected an error for a commit that does not exist")
	}
}

func hangingGitServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	return server
}

func TestReadingTheTrunkCommitGivesUpOnARemoteThatNeverResponds(t *testing.T) {
	server := hangingGitServer(t)
	project := domain.Project{Name: "example-project", CloneUrl: server.URL + "/repository.git", TrunkBranch: "master"}
	gitStore := GitRepositoryStore{ProjectsDirectory: t.TempDir(), NetworkTimeLimit: 300 * time.Millisecond}

	startedAt := time.Now()
	_, err := gitStore.ReadCurrentTrunkCommit(context.Background(), project)

	if err == nil {
		t.Fatal("expected an error from a remote that never responds")
	}
	if elapsed := time.Since(startedAt); elapsed > 10*time.Second {
		t.Errorf("giving up took %s", elapsed)
	}
}

func TestCheckoutGivesUpOnARemoteThatNeverResponds(t *testing.T) {
	server := hangingGitServer(t)
	project := domain.Project{Name: "example-project", CloneUrl: server.URL + "/repository.git", TrunkBranch: "master"}
	gitStore := GitRepositoryStore{ProjectsDirectory: t.TempDir(), NetworkTimeLimit: 300 * time.Millisecond}

	startedAt := time.Now()
	_, err := gitStore.PrepareCleanCheckoutAtCommit(context.Background(), project, strings.Repeat("a", 40))

	if err == nil {
		t.Fatal("expected an error from a remote that never responds")
	}
	if elapsed := time.Since(startedAt); elapsed > 10*time.Second {
		t.Errorf("giving up took %s", elapsed)
	}
}

func TestRemoteAskingForCredentialsFailsRatherThanWaitingForAnswer(t *testing.T) {
	t.Setenv("GIT_ASKPASS", "")
	t.Setenv("SSH_ASKPASS", "")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("WWW-Authenticate", `Basic realm="example"`)
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	project := domain.Project{Name: "example-project", CloneUrl: server.URL + "/repository.git", TrunkBranch: "master"}

	_, err := newGitRepositoryStore(t).ReadCurrentTrunkCommit(context.Background(), project)

	if err == nil || !strings.Contains(err.Error(), "terminal prompts disabled") {
		t.Fatalf("expected git to refuse to prompt, got %v", err)
	}
}
