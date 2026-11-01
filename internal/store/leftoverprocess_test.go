package store

import (
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// No test here may build the stopper with NewLeftoverProcessStopper. citask enrols itself, so these
// tests run as _ci inside a live run, where the real kill path would stop that run.

const psOutputOfATypicalRun = `
root         1     0
root       100     1
_ci        200   100
_ci        201   200
_ci        300   201
_ci        400   300
_ci        401   400
_ci        500     1
someone    600     1
someone    601   600
`

func TestSelectionKeepsCitaskAndItsAncestorsAndSelectsOtherCiProcesses(t *testing.T) {
	selected := selectLeftoverProcessIdentifiers(psOutputOfATypicalRun, 300)

	if got := joinProcessIdentifiers(selected); got != "400,401,500" {
		t.Errorf("selected %s", got)
	}
}

func TestSelectionKeepsTheProcessListerWhichAlwaysAppearsInItsOwnListing(t *testing.T) {
	selected := selectLeftoverProcessIdentifiers("_ci 300 1\n_ci 301 300\n", 300, 301)

	if len(selected) != 0 {
		t.Errorf("selected %v", selected)
	}
}

func TestSelectionNeverIncludesAnotherUsersProcess(t *testing.T) {
	selected := selectLeftoverProcessIdentifiers(psOutputOfATypicalRun, 300)

	for _, processIdentifier := range selected {
		if processIdentifier == 1 || processIdentifier == 100 || processIdentifier == 600 || processIdentifier == 601 {
			t.Errorf("selected another user's process %d", processIdentifier)
		}
	}
}

func TestSelectionTreatsAUsernameThatOnlyResemblesCiAsAnotherUser(t *testing.T) {
	selected := selectLeftoverProcessIdentifiers("_ci2 900 1\n_CI 901 1\nci 902 1\n", 300)

	if len(selected) != 0 {
		t.Errorf("selected %v", selected)
	}
}

func TestSelectionIgnoresLinesItCannotRead(t *testing.T) {
	selected := selectLeftoverProcessIdentifiers("not a process\n_ci 700\n_ci seven hundred\n\n_ci 701 1\n", 300)

	if got := joinProcessIdentifiers(selected); got != "701" {
		t.Errorf("selected %s", got)
	}
}

func TestSelectionWithAParentCycleStillFinishes(t *testing.T) {
	selected := selectLeftoverProcessIdentifiers("_ci 300 301\n_ci 301 300\n_ci 302 1\n", 300)

	if got := joinProcessIdentifiers(selected); got != "302" {
		t.Errorf("selected %s", got)
	}
}

func joinProcessIdentifiers(processIdentifiers []int) string {
	var joined []string
	for _, processIdentifier := range processIdentifiers {
		joined = append(joined, strconv.Itoa(processIdentifier))
	}
	return strings.Join(joined, ",")
}

const processIdentifierOfTheFakeLister = 999

type sentSignal struct {
	ProcessIdentifier int
	Signal            syscall.Signal
}

type fakeProcessTable struct {
	listings      []string
	listCallCount int
	sentSignals   []sentSignal
}

func (table *fakeProcessTable) listProcesses() (string, int, error) {
	listing := table.listings[min(table.listCallCount, len(table.listings)-1)]
	table.listCallCount++
	return listing, processIdentifierOfTheFakeLister, nil
}

func (table *fakeProcessTable) sendSignal(processIdentifier int, signal syscall.Signal) error {
	table.sentSignals = append(table.sentSignals, sentSignal{ProcessIdentifier: processIdentifier, Signal: signal})
	return nil
}

func stopperWithFakeProcessTable(username string, table *fakeProcessTable) LeftoverProcessStopper {
	return LeftoverProcessStopper{
		currentUsername:          func() (string, error) { return username, nil },
		currentProcessIdentifier: 300,
		listProcesses:            table.listProcesses,
		sendSignal:               table.sendSignal,
		gracePeriod:              20 * time.Millisecond,
		intervalBetweenChecks:    time.Millisecond,
	}
}

func TestStopperRefusesUnderAnyUsernameOtherThanCi(t *testing.T) {
	for _, username := range []string{"", "root", "someone", "ci", "_ci2", "_CI", " _ci", "_ci "} {
		t.Run(username, func(t *testing.T) {
			table := &fakeProcessTable{listings: []string{psOutputOfATypicalRun}}

			err := stopperWithFakeProcessTable(username, table).StopLeftoverProcesses()

			if err == nil {
				t.Error("the stopper should refuse")
			}
			if table.listCallCount != 0 {
				t.Error("the stopper should not even list processes")
			}
			if len(table.sentSignals) != 0 {
				t.Errorf("the stopper sent signals: %+v", table.sentSignals)
			}
		})
	}
}

func TestStopperRefusesWhenTheUsernameCannotBeRead(t *testing.T) {
	table := &fakeProcessTable{listings: []string{psOutputOfATypicalRun}}
	stopper := stopperWithFakeProcessTable("_ci", table)
	stopper.currentUsername = func() (string, error) { return "", errors.New("no such user") }

	if err := stopper.StopLeftoverProcesses(); err == nil {
		t.Error("the stopper should refuse")
	}
	if len(table.sentSignals) != 0 {
		t.Errorf("the stopper sent signals: %+v", table.sentSignals)
	}
}

func TestStopperTerminatesLeftoversAndKillsOnlyThoseStillRunningAfterTheGracePeriod(t *testing.T) {
	stillRunning := `
_ci 200 100
_ci 201 200
_ci 300 201
_ci 500 1
`
	table := &fakeProcessTable{listings: []string{psOutputOfATypicalRun, stillRunning}}

	if err := stopperWithFakeProcessTable("_ci", table).StopLeftoverProcesses(); err != nil {
		t.Fatal(err)
	}

	terminated, killed := []int{}, []int{}
	for _, sent := range table.sentSignals {
		switch sent.Signal {
		case syscall.SIGTERM:
			terminated = append(terminated, sent.ProcessIdentifier)
		case syscall.SIGKILL:
			killed = append(killed, sent.ProcessIdentifier)
		default:
			t.Errorf("unexpected signal: %+v", sent)
		}
	}
	if got := joinProcessIdentifiers(terminated); got != "400,401,500" {
		t.Errorf("terminated %s", got)
	}
	if got := joinProcessIdentifiers(killed); got != "500" {
		t.Errorf("killed %s", got)
	}
}

func TestStopperWithNothingLeftOverSendsNoSignals(t *testing.T) {
	table := &fakeProcessTable{listings: []string{"root 1 0\n_ci 300 1\n_ci 999 300\n"}}

	if err := stopperWithFakeProcessTable("_ci", table).StopLeftoverProcesses(); err != nil {
		t.Fatal(err)
	}

	if len(table.sentSignals) != 0 {
		t.Errorf("signals: %+v", table.sentSignals)
	}
}

func TestStopperIgnoresAProcessThatExitedBeforeItsSignalArrived(t *testing.T) {
	table := &fakeProcessTable{listings: []string{psOutputOfATypicalRun, "_ci 300 1\n"}}
	stopper := stopperWithFakeProcessTable("_ci", table)
	stopper.sendSignal = func(processIdentifier int, signal syscall.Signal) error { return syscall.ESRCH }

	if err := stopper.StopLeftoverProcesses(); err != nil {
		t.Fatal(err)
	}
}

func TestStopperReportsASignalThatIsRefused(t *testing.T) {
	table := &fakeProcessTable{listings: []string{psOutputOfATypicalRun}}
	stopper := stopperWithFakeProcessTable("_ci", table)
	stopper.sendSignal = func(processIdentifier int, signal syscall.Signal) error { return syscall.EPERM }

	if err := stopper.StopLeftoverProcesses(); err == nil {
		t.Error("expected the refusal to be reported")
	}
}

func TestStopperReportsAProcessListingThatFails(t *testing.T) {
	table := &fakeProcessTable{listings: []string{psOutputOfATypicalRun}}
	stopper := stopperWithFakeProcessTable("_ci", table)
	stopper.listProcesses = func() (string, int, error) { return "", 0, errors.New("ps failed") }

	if err := stopper.StopLeftoverProcesses(); err == nil {
		t.Error("expected the failure to be reported")
	}
	if len(table.sentSignals) != 0 {
		t.Errorf("signals: %+v", table.sentSignals)
	}
}
