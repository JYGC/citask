package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

const shortStopGracePeriod = 200 * time.Millisecond

func newStepEnvironment(t *testing.T) domain.StepEnvironment {
	t.Helper()
	privateTemporaryFolder := t.TempDir()
	if err := os.Mkdir(filepath.Join(privateTemporaryFolder, xdgRuntimeFolderName), 0o700); err != nil {
		t.Fatal(err)
	}
	return domain.StepEnvironment{
		CheckoutDirectory:      t.TempDir(),
		PrivateTemporaryFolder: privateTemporaryFolder,
		RunLogFile:             filepath.Join(t.TempDir(), "run.log"),
		RunArtifactDirectory:   t.TempDir(),
	}
}

func shellStepDefinition(script string) domain.StepDefinition {
	return domain.StepDefinition{Name: "example-step", WorkingDirectory: ".", Command: []string{"sh", "-c", script}}
}

func runLogContent(t *testing.T, environment domain.StepEnvironment) string {
	t.Helper()
	content, err := os.ReadFile(environment.RunLogFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func runStepWithinLimit(t *testing.T, step domain.StepDefinition, environment domain.StepEnvironment, limit time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	return StepProcessStore{ProcessStopGracePeriod: shortStopGracePeriod}.RunStep(ctx, step, environment)
}

func TestStepOutputGoesToTheRunLogAfterAnythingAlreadyThere(t *testing.T) {
	environment := newStepEnvironment(t)
	if err := os.WriteFile(environment.RunLogFile, []byte("already there\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runStepWithinLimit(t, shellStepDefinition("echo to-standard-output; echo to-standard-error >&2"), environment, time.Minute)

	if err != nil {
		t.Fatal(err)
	}
	runLog := runLogContent(t, environment)
	for _, expected := range []string{"already there", "to-standard-output", "to-standard-error"} {
		if !strings.Contains(runLog, expected) {
			t.Errorf("expected %q in:\n%s", expected, runLog)
		}
	}
}

func TestStepThatExitsNonZeroReturnsAnError(t *testing.T) {
	err := runStepWithinLimit(t, shellStepDefinition("exit 3"), newStepEnvironment(t), time.Minute)

	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, domain.ErrStepStoppedByRunTimeLimit) {
		t.Fatal("an ordinary failure is not a time limit")
	}
}

func TestStepWhoseProgramDoesNotExistReturnsAnError(t *testing.T) {
	step := domain.StepDefinition{Name: "example-step", WorkingDirectory: ".", Command: []string{"program-that-is-not-installed"}}

	if err := runStepWithinLimit(t, step, newStepEnvironment(t), time.Minute); err == nil {
		t.Fatal("expected an error")
	}
}

func TestStepRunsInItsWorkingDirectoryInsideTheCheckout(t *testing.T) {
	environment := newStepEnvironment(t)
	if err := os.Mkdir(filepath.Join(environment.CheckoutDirectory, "frontend"), 0o755); err != nil {
		t.Fatal(err)
	}
	step := shellStepDefinition("pwd -P")
	step.WorkingDirectory = "frontend"

	if err := runStepWithinLimit(t, step, environment, time.Minute); err != nil {
		t.Fatal(err)
	}

	expectedDirectory, err := filepath.EvalSymlinks(filepath.Join(environment.CheckoutDirectory, "frontend"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runLogContent(t, environment)); got != expectedDirectory {
		t.Errorf("got %q, want %q", got, expectedDirectory)
	}
}

func TestStepEnvironmentIsTheAllowlistPlusItsOwnVariables(t *testing.T) {
	t.Setenv("HOME", "/example/home")
	t.Setenv("GITHUB_TOKEN", "fake-token-for-tests-only")
	t.Setenv("VARIABLE_OUTSIDE_THE_ALLOWLIST", "must-not-reach-a-step")
	environment := newStepEnvironment(t)
	step := domain.StepDefinition{
		Name:             "print-environment",
		WorkingDirectory: ".",
		Command:          []string{"env"},
		Environment:      map[string]string{"STEP_VARIABLE": "step-value", "CI": "overridden-by-the-step"},
	}

	if err := runStepWithinLimit(t, step, environment, time.Minute); err != nil {
		t.Fatal(err)
	}

	runLog := runLogContent(t, environment)
	for _, expected := range []string{
		"HOME=/example/home",
		"STEP_VARIABLE=step-value",
		"CI=overridden-by-the-step",
		"TMPDIR=" + environment.PrivateTemporaryFolder,
		"XDG_RUNTIME_DIR=" + filepath.Join(environment.PrivateTemporaryFolder, xdgRuntimeFolderName),
		"CI_RUN_ARTIFACT_DIRECTORY=" + environment.RunArtifactDirectory,
	} {
		if !strings.Contains(runLog, expected) {
			t.Errorf("expected %q in:\n%s", expected, runLog)
		}
	}
	for _, forbidden := range []string{"GITHUB_TOKEN", "fake-token-for-tests-only", "VARIABLE_OUTSIDE_THE_ALLOWLIST"} {
		if strings.Contains(runLog, forbidden) {
			t.Errorf("did not expect %q in:\n%s", forbidden, runLog)
		}
	}
}

func TestStepEnvironmentSetsCiTrueByDefault(t *testing.T) {
	environment := newStepEnvironment(t)
	step := domain.StepDefinition{Name: "print-environment", WorkingDirectory: ".", Command: []string{"env"}}

	if err := runStepWithinLimit(t, step, environment, time.Minute); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(runLogContent(t, environment), "CI=true") {
		t.Error("expected CI=true")
	}
}

func TestStepPastTheLimitReceivesTerminateAndCanCleanUp(t *testing.T) {
	environment := newStepEnvironment(t)
	step := shellStepDefinition(`trap 'echo cleaned-up-after-terminate; exit 0' TERM; sleep 60 & wait`)

	err := runStepWithinLimit(t, step, environment, 300*time.Millisecond)

	if !errors.Is(err, domain.ErrStepStoppedByRunTimeLimit) {
		t.Fatalf("expected the time limit error even after a clean exit, got %v", err)
	}
	if !strings.Contains(runLogContent(t, environment), "cleaned-up-after-terminate") {
		t.Error("the step should have had the chance to clean up")
	}
}

func readProcessIdentifier(t *testing.T, path string) int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	processIdentifier, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		t.Fatal(err)
	}
	return processIdentifier
}

func assertProcessIsGone(t *testing.T, processIdentifier int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(processIdentifier, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			syscall.Kill(processIdentifier, syscall.SIGKILL)
			t.Fatalf("process %d survived", processIdentifier)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStepIgnoringTerminateIsKilledAfterTheGracePeriod(t *testing.T) {
	environment := newStepEnvironment(t)
	childFile := filepath.Join(t.TempDir(), "child")
	step := shellStepDefinition(fmt.Sprintf(`trap '' TERM; sleep 60 & echo $! > %q; wait`, childFile))

	startedAt := time.Now()
	err := runStepWithinLimit(t, step, environment, 300*time.Millisecond)

	if !errors.Is(err, domain.ErrStepStoppedByRunTimeLimit) {
		t.Fatalf("got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 10*time.Second {
		t.Errorf("stopping took %s", elapsed)
	}
	assertProcessIsGone(t, readProcessIdentifier(t, childFile))
}

func TestChildThatOutlivesItsStepLeaderIsStoppedWithTheGroup(t *testing.T) {
	environment := newStepEnvironment(t)
	childFile := filepath.Join(t.TempDir(), "child")
	step := shellStepDefinition(fmt.Sprintf(`(trap '' TERM; sleep 60) & echo $! > %q; wait`, childFile))

	err := runStepWithinLimit(t, step, environment, 300*time.Millisecond)

	if !errors.Is(err, domain.ErrStepStoppedByRunTimeLimit) {
		t.Fatalf("got %v", err)
	}
	assertProcessIsGone(t, readProcessIdentifier(t, childFile))
}

func TestBackgroundChildDoesNotKeepAFinishedStepWaiting(t *testing.T) {
	environment := newStepEnvironment(t)
	childFile := filepath.Join(t.TempDir(), "child")
	step := shellStepDefinition(fmt.Sprintf(`sleep 60 & echo $! > %q`, childFile))

	startedAt := time.Now()
	err := runStepWithinLimit(t, step, environment, time.Minute)

	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed > 10*time.Second {
		t.Errorf("the step waited %s for its background child", elapsed)
	}
	syscall.Kill(readProcessIdentifier(t, childFile), syscall.SIGKILL)
}

func TestPrivateTemporaryFolderIsNamedAfterTheProjectAndCommitAndIsPrivate(t *testing.T) {
	folders := PrivateTemporaryFolderStore{ParentDirectory: t.TempDir()}

	folder, err := folders.CreatePrivateTemporaryFolder(domain.Project{Name: "example-project"}, "094b1e7c00000000000000000000000000000000")

	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(folder), "citask-example-project-094b1e7-") {
		t.Errorf("unexpected name: %s", folder)
	}
	if filepath.Dir(folder) != folders.ParentDirectory {
		t.Errorf("unexpected parent: %s", folder)
	}
	folderInformation, err := os.Stat(folder)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := folderInformation.Mode().Perm(); permissions != 0o700 {
		t.Errorf("folder permissions: %o", permissions)
	}
	runtimeFolderInformation, err := os.Stat(filepath.Join(folder, xdgRuntimeFolderName))
	if err != nil {
		t.Fatal(err)
	}
	if permissions := runtimeFolderInformation.Mode().Perm(); permissions != 0o700 {
		t.Errorf("runtime folder permissions: %o", permissions)
	}
}

func TestDeletingAPrivateTemporaryFolderLeavesItsNeighbours(t *testing.T) {
	folders := PrivateTemporaryFolderStore{ParentDirectory: t.TempDir()}
	neighbour := filepath.Join(folders.ParentDirectory, "neighbour")
	if err := os.WriteFile(neighbour, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	folder, err := folders.CreatePrivateTemporaryFolder(domain.Project{Name: "example-project"}, "094b1e7c00000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "left-behind"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := folders.DeletePrivateTemporaryFolder(folder); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("the folder survived: %v", err)
	}
	if _, err := os.Stat(neighbour); err != nil {
		t.Errorf("the neighbour was removed: %v", err)
	}
}

func TestDeletingRefusesAnythingThatIsNotAPrivateTemporaryFolderOfTheParent(t *testing.T) {
	folders := PrivateTemporaryFolderStore{ParentDirectory: t.TempDir()}
	otherFolder := filepath.Join(folders.ParentDirectory, "not-a-citask-folder")
	if err := os.Mkdir(otherFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	folderElsewhere := filepath.Join(t.TempDir(), "citask-elsewhere")
	if err := os.Mkdir(folderElsewhere, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, refusedPath := range []string{"", "/", folders.ParentDirectory, otherFolder, folderElsewhere} {
		if err := folders.DeletePrivateTemporaryFolder(refusedPath); err == nil {
			t.Errorf("deleting %q should be refused", refusedPath)
		}
	}
	for _, survivor := range []string{otherFolder, folderElsewhere} {
		if _, err := os.Stat(survivor); err != nil {
			t.Errorf("%s was removed: %v", survivor, err)
		}
	}
}
