package store

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const lockFileForHelperProcess = "CITASK_TEST_LOCK_FILE_FOR_HELPER_PROCESS"

func TestLockIsRefusedWhileHeldAndGrantedOnceReleased(t *testing.T) {
	lockFile := filepath.Join(t.TempDir(), "state", "citask.lock")

	releaseFirstHold, acquired, err := ExclusivePollFileLock{LockFile: lockFile}.TryAcquireExclusivePollLock()
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("a free lock should be granted")
	}

	_, acquired, err = ExclusivePollFileLock{LockFile: lockFile}.TryAcquireExclusivePollLock()
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		t.Fatal("a held lock should be refused")
	}

	if err := releaseFirstHold(); err != nil {
		t.Fatal(err)
	}
	releaseSecondHold, acquired, err := ExclusivePollFileLock{LockFile: lockFile}.TryAcquireExclusivePollLock()
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("a released lock should be granted")
	}
	if err := releaseSecondHold(); err != nil {
		t.Fatal(err)
	}
}

func TestLockReportsAFolderItCannotCreate(t *testing.T) {
	fileWhereAFolderIsNeeded := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileWhereAFolderIsNeeded, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	lock := ExclusivePollFileLock{LockFile: filepath.Join(fileWhereAFolderIsNeeded, "citask.lock")}
	if _, _, err := lock.TryAcquireExclusivePollLock(); err == nil {
		t.Fatal("expected an error")
	}
}

func TestHelperProcessThatHoldsTheLockAndStartsAChild(t *testing.T) {
	lockFile := os.Getenv(lockFileForHelperProcess)
	if lockFile == "" {
		t.Skip("this only does something when started as a helper process")
	}
	_, acquired, err := ExclusivePollFileLock{LockFile: lockFile}.TryAcquireExclusivePollLock()
	if err != nil || !acquired {
		t.Fatalf("acquired %v, err %v", acquired, err)
	}
	childStandingInForARunningStep := exec.Command("sleep", "60")
	if err := childStandingInForARunningStep.Start(); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("child-process-identifier=%d\n", childStandingInForARunningStep.Process.Pid)
	time.Sleep(5 * time.Minute)
}

func TestLockIsReleasedWhenItsHolderDiesEvenIfAChildOfTheHolderLivesOn(t *testing.T) {
	lockFile := filepath.Join(t.TempDir(), "citask.lock")
	helperProcess := exec.Command(os.Args[0], "-test.run=^TestHelperProcessThatHoldsTheLockAndStartsAChild$")
	helperProcess.Env = append(os.Environ(), lockFileForHelperProcess+"="+lockFile)
	helperOutput, err := helperProcess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helperProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { helperProcess.Process.Kill() })

	firstLine, err := bufio.NewReader(helperOutput).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	childProcessIdentifier, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(firstLine), "child-process-identifier="))
	if err != nil {
		t.Fatalf("unexpected helper output %q: %v", firstLine, err)
	}
	t.Cleanup(func() { syscall.Kill(childProcessIdentifier, syscall.SIGKILL) })

	_, acquired, err := ExclusivePollFileLock{LockFile: lockFile}.TryAcquireExclusivePollLock()
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		t.Fatal("the helper should be holding the lock")
	}

	if err := helperProcess.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	helperProcess.Wait()

	if err := syscall.Kill(childProcessIdentifier, 0); err != nil {
		t.Fatalf("the child should have outlived the holder: %v", err)
	}
	releaseLock, acquired, err := ExclusivePollFileLock{LockFile: lockFile}.TryAcquireExclusivePollLock()
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("the lock should be free once its holder has died, even with a child still alive")
	}
	if err := releaseLock(); err != nil {
		t.Fatal(err)
	}
}
