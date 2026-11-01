package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

const (
	xdgRuntimeFolderName                 = "xdg-runtime"
	privateTemporaryFolderPrefix         = "citask-"
	intervalBetweenChecksThatGroupIsGone = 50 * time.Millisecond
)

var variablesInheritedByEveryStep = []string{"HOME", "PATH", "USER", "LOGNAME", "LANG", "LC_ALL"}

type StepProcessStore struct {
	ProcessStopGracePeriod time.Duration
}

func (stepProcessStore StepProcessStore) RunStep(ctx context.Context, step domain.StepDefinition, environment domain.StepEnvironment) error {
	runLogFile, err := os.OpenFile(environment.RunLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open run log: %w", err)
	}
	defer runLogFile.Close()

	command := exec.Command(step.Command[0], step.Command[1:]...)
	command.Dir = filepath.Join(environment.CheckoutDirectory, step.WorkingDirectory)
	command.Env = environmentVariablesForStep(step, environment)
	command.Stdout = runLogFile
	command.Stderr = runLogFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return err
	}

	leaderFinished := make(chan error, 1)
	go func() { leaderFinished <- command.Wait() }()
	select {
	case err := <-leaderFinished:
		return err
	case <-ctx.Done():
		stepProcessStore.stopProcessGroup(command.Process.Pid, leaderFinished)
		return domain.ErrStepStoppedByRunTimeLimit
	}
}

func environmentVariablesForStep(step domain.StepDefinition, environment domain.StepEnvironment) []string {
	variables := make([]string, 0, len(variablesInheritedByEveryStep)+len(step.Environment)+4)
	for _, variableName := range variablesInheritedByEveryStep {
		if value, isSet := os.LookupEnv(variableName); isSet {
			variables = append(variables, variableName+"="+value)
		}
	}
	variables = append(variables,
		"CI=true",
		"TMPDIR="+environment.PrivateTemporaryFolder,
		"XDG_RUNTIME_DIR="+filepath.Join(environment.PrivateTemporaryFolder, xdgRuntimeFolderName),
		"CI_RUN_ARTIFACT_DIRECTORY="+environment.RunArtifactDirectory,
	)
	for variableName, value := range step.Environment {
		variables = append(variables, variableName+"="+value)
	}
	return variables
}

func (stepProcessStore StepProcessStore) stopProcessGroup(processGroupIdentifier int, leaderFinished <-chan error) {
	syscall.Kill(-processGroupIdentifier, syscall.SIGTERM)
	gracePeriodOver := time.After(stepProcessStore.ProcessStopGracePeriod)
	for {
		select {
		case <-leaderFinished:
			leaderFinished = nil
		case <-gracePeriodOver:
			syscall.Kill(-processGroupIdentifier, syscall.SIGKILL)
			if leaderFinished != nil {
				<-leaderFinished
			}
			return
		case <-time.After(intervalBetweenChecksThatGroupIsGone):
		}
		if leaderFinished == nil && errors.Is(syscall.Kill(-processGroupIdentifier, 0), syscall.ESRCH) {
			return
		}
	}
}

type PrivateTemporaryFolderStore struct {
	ParentDirectory string
}

func (folders PrivateTemporaryFolderStore) CreatePrivateTemporaryFolder(project domain.Project, commitSha string) (string, error) {
	namePattern := fmt.Sprintf("%s%s-%s-*", privateTemporaryFolderPrefix, project.Name, domain.ShortCommitSha(commitSha))
	folder, err := os.MkdirTemp(folders.ParentDirectory, namePattern)
	if err != nil {
		return "", fmt.Errorf("create private temporary folder: %w", err)
	}
	if err := os.Mkdir(filepath.Join(folder, xdgRuntimeFolderName), 0o700); err != nil {
		return "", fmt.Errorf("create runtime folder: %w", err)
	}
	return folder, nil
}

func (folders PrivateTemporaryFolderStore) DeletePrivateTemporaryFolder(folder string) error {
	if filepath.Dir(folder) != folders.ParentDirectory || !strings.HasPrefix(filepath.Base(folder), privateTemporaryFolderPrefix) {
		return fmt.Errorf("refusing to delete %q, which is not a private temporary folder in %s", folder, folders.ParentDirectory)
	}
	return os.RemoveAll(folder)
}
