package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

const waitForGitOutputAfterStopping = time.Second

var fullCommitSha = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

type GitRepositoryStore struct {
	ProjectsDirectory string
	NetworkTimeLimit  time.Duration
}

func (gitStore GitRepositoryStore) ReadCurrentTrunkCommit(ctx context.Context, project domain.Project) (string, error) {
	trunkRef := "refs/heads/" + project.TrunkBranch
	networkContext, cancel := context.WithTimeout(ctx, gitStore.NetworkTimeLimit)
	defer cancel()
	output, err := runGit(networkContext, "", "ls-remote", "--", project.CloneUrl, trunkRef)
	if err != nil {
		return "", err
	}
	return commitShaOfExactRef(output, trunkRef)
}

// commitShaOfExactRef is needed because git ls-remote matches a pattern by its tail, so asking for one
// ref can also return a longer ref that ends with it.
func commitShaOfExactRef(lsRemoteOutput, exactRef string) (string, error) {
	for _, line := range strings.Split(lsRemoteOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == exactRef {
			if !fullCommitSha.MatchString(fields[0]) {
				return "", fmt.Errorf("git ls-remote returned %q for %s, which is not a commit", fields[0], exactRef)
			}
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("the remote has no %s", exactRef)
}

func (gitStore GitRepositoryStore) PrepareCleanCheckoutAtCommit(ctx context.Context, project domain.Project, commitSha string) (string, error) {
	checkoutDirectory := filepath.Join(gitStore.ProjectsDirectory, project.Name)
	if err := os.MkdirAll(checkoutDirectory, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", checkoutDirectory, err)
	}
	if _, err := runGit(ctx, checkoutDirectory, "init", "--quiet"); err != nil {
		return "", err
	}

	networkContext, cancel := context.WithTimeout(ctx, gitStore.NetworkTimeLimit)
	defer cancel()
	refspec := fmt.Sprintf("+refs/heads/%[1]s:refs/remotes/trunk/%[1]s", project.TrunkBranch)
	if _, err := runGit(networkContext, checkoutDirectory, "fetch", "--quiet", "--force", "--", project.CloneUrl, refspec); err != nil {
		return "", err
	}

	if _, err := runGit(ctx, checkoutDirectory, "checkout", "--quiet", "--force", "--detach", commitSha); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, checkoutDirectory, "clean", "--quiet", "-ffdx"); err != nil {
		return "", err
	}
	return checkoutDirectory, nil
}

// runGit stops the whole process group because git leaves HTTP to a child process that holds the output
// pipes. Stopping only git would leave Wait blocked on those pipes, and a hung fetch would hold the poll
// lock forever. WaitDelay bounds that wait if a child still escapes the group.
func runGit(ctx context.Context, directory string, arguments ...string) (string, error) {
	var standardOutput, standardError bytes.Buffer
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	command.Stdout = &standardOutput
	command.Stderr = &standardError
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = waitForGitOutputAfterStopping

	err := command.Run()
	if contextError := ctx.Err(); contextError != nil {
		return "", fmt.Errorf("git %s stopped: %w", arguments[0], contextError)
	}
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return "", fmt.Errorf("git %s: %w: %s", arguments[0], err, strings.TrimSpace(standardError.String()))
	}
	return standardOutput.String(), nil
}
