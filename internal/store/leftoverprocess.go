package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// leftoverProcessOwner is deliberately a constant rather than a config value, so a config mistake cannot
// point this sweep at a real account. _ci runs one thing at a time and is used for nothing else, so
// every _ci process other than citask and its ancestors belongs to the run.
const leftoverProcessOwner = "_ci"

const intervalBetweenChecksThatLeftoversHaveStopped = 100 * time.Millisecond

type LeftoverProcessStopper struct {
	currentUsername          func() (string, error)
	currentProcessIdentifier int
	listProcesses            func() (processListing string, listerProcessIdentifier int, err error)
	sendSignal               func(processIdentifier int, signal syscall.Signal) error
	gracePeriod              time.Duration
	intervalBetweenChecks    time.Duration
}

// NewLeftoverProcessStopper builds the real stopper. Only cmd/citask/main.go may call it, because
// citask's own tests run as _ci inside a live run, where a real sweep would stop that run.
func NewLeftoverProcessStopper(gracePeriod time.Duration) LeftoverProcessStopper {
	return LeftoverProcessStopper{
		currentUsername:          readCurrentUsername,
		currentProcessIdentifier: os.Getpid(),
		listProcesses:            listProcessesWithPs,
		sendSignal:               syscall.Kill,
		gracePeriod:              gracePeriod,
		intervalBetweenChecks:    intervalBetweenChecksThatLeftoversHaveStopped,
	}
}

func readCurrentUsername() (string, error) {
	currentUser, err := user.Current()
	if err != nil {
		return "", err
	}
	return currentUser.Username, nil
}

func listProcessesWithPs() (string, int, error) {
	var standardOutput bytes.Buffer
	command := exec.Command("ps", "-ax", "-o", "user=", "-o", "pid=", "-o", "ppid=")
	command.Stdout = &standardOutput
	if err := command.Start(); err != nil {
		return "", 0, fmt.Errorf("list processes: %w", err)
	}
	listerProcessIdentifier := command.Process.Pid
	if err := command.Wait(); err != nil {
		return "", 0, fmt.Errorf("list processes: %w", err)
	}
	return standardOutput.String(), listerProcessIdentifier, nil
}

func (stopper LeftoverProcessStopper) StopLeftoverProcesses() error {
	username, err := stopper.currentUsername()
	if err != nil {
		return fmt.Errorf("refusing to stop processes, because the current username cannot be read: %w", err)
	}
	if username != leftoverProcessOwner {
		return fmt.Errorf("refusing to stop processes as %q, because that is only safe as %q", username, leftoverProcessOwner)
	}

	leftovers, err := stopper.findLeftovers()
	if err != nil {
		return err
	}
	if err := stopper.signalEveryProcess(leftovers, syscall.SIGTERM); err != nil {
		return err
	}
	for deadline := time.Now().Add(stopper.gracePeriod); len(leftovers) > 0 && time.Now().Before(deadline); {
		time.Sleep(stopper.intervalBetweenChecks)
		if leftovers, err = stopper.findLeftovers(); err != nil {
			return err
		}
	}
	return stopper.signalEveryProcess(leftovers, syscall.SIGKILL)
}

func (stopper LeftoverProcessStopper) findLeftovers() ([]int, error) {
	processListing, listerProcessIdentifier, err := stopper.listProcesses()
	if err != nil {
		return nil, err
	}
	return selectLeftoverProcessIdentifiers(processListing, stopper.currentProcessIdentifier, listerProcessIdentifier), nil
}

func (stopper LeftoverProcessStopper) signalEveryProcess(processIdentifiers []int, signal syscall.Signal) error {
	var signalErrors []error
	for _, processIdentifier := range processIdentifiers {
		err := stopper.sendSignal(processIdentifier, signal)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			signalErrors = append(signalErrors, fmt.Errorf("signal process %d: %w", processIdentifier, err))
		}
	}
	return errors.Join(signalErrors...)
}

type listedProcess struct {
	owner                   string
	processIdentifier       int
	parentProcessIdentifier int
}

func parseProcessListing(processListing string) []listedProcess {
	processes := []listedProcess{}
	for _, line := range strings.Split(processListing, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		processIdentifier, processIdentifierError := strconv.Atoi(fields[1])
		parentProcessIdentifier, parentProcessIdentifierError := strconv.Atoi(fields[2])
		if processIdentifierError != nil || parentProcessIdentifierError != nil {
			continue
		}
		processes = append(processes, listedProcess{fields[0], processIdentifier, parentProcessIdentifier})
	}
	return processes
}

func selectLeftoverProcessIdentifiers(processListing string, processIdentifiersToKeep ...int) []int {
	processes := parseProcessListing(processListing)
	parentOf := map[int]int{}
	for _, process := range processes {
		parentOf[process.processIdentifier] = process.parentProcessIdentifier
	}

	keptProcessIdentifiers := map[int]bool{}
	for _, processIdentifier := range processIdentifiersToKeep {
		for processIdentifier != 0 && !keptProcessIdentifiers[processIdentifier] {
			keptProcessIdentifiers[processIdentifier] = true
			processIdentifier = parentOf[processIdentifier]
		}
	}

	selected := []int{}
	for _, process := range processes {
		if process.owner == leftoverProcessOwner && !keptProcessIdentifiers[process.processIdentifier] {
			selected = append(selected, process.processIdentifier)
		}
	}
	sort.Ints(selected)
	return selected
}
