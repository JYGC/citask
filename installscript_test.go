package citask_test

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	validConfigurationContent = "{\"Example\": \"valid configuration\"}\n"
	scheduleEntry             = "*/5 * * * * _ci -n -s /usr/local/libexec/citask/citask poll"
	fakeTokenContent          = "fake-token-for-tests-only\n"
)

// fakeProgram stands in for the built citask. Its check-config answers like the real one, which
// cmd/citask tests directly.
const fakeProgram = `#!/bin/sh
if [ "$1" = check-config ] && grep -q invalid "$2"; then
	echo "citask: ExampleField is invalid" >&2
	exit 1
fi
`

// installFixture runs the script against a throwaway root. The stubs stand in for the three commands
// that need real root, so these tests prove the script's logic. Running it on the machine proves the rest.
type installFixture struct {
	t                   *testing.T
	root                string
	stubDirectory       string
	callLog             string
	programSource       string
	configurationSource string
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFileInFixture(t *testing.T, path, content string, permissions os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), permissions); err != nil {
		t.Fatal(err)
	}
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	fixture := &installFixture{
		t:             t,
		root:          t.TempDir(),
		stubDirectory: t.TempDir(),
	}
	sources := t.TempDir()
	fixture.callLog = filepath.Join(sources, "calls.log")
	fixture.programSource = filepath.Join(sources, "citask")
	fixture.configurationSource = filepath.Join(sources, "citask.json")
	writeExecutable(t, fixture.programSource, fakeProgram)
	writeFileInFixture(t, fixture.configurationSource, validConfigurationContent, 0o644)

	writeFileInFixture(t, fixture.inRoot("etc/master.passwd"), "root:*:0:0:daemon:0:0:Charlie &:/root:/bin/ksh\n", 0o600)
	writeFileInFixture(t, fixture.inRoot("etc/group"), "wheel:*:0:root,someone\n", 0o644)
	writeFileInFixture(t, fixture.inRoot("var/cron/cron.deny"), "", 0o660)
	writeFileInFixture(t, fixture.inRoot("var/cron/at.deny"), "", 0o660)

	writeExecutable(t, filepath.Join(fixture.stubDirectory, "id"), "#!/bin/sh\necho 0\n")
	writeExecutable(t, filepath.Join(fixture.stubDirectory, "chown"), "#!/bin/sh\necho \"chown $*\" >> \"$CITASK_TEST_CALL_LOG\"\n")
	writeExecutable(t, filepath.Join(fixture.stubDirectory, "useradd"), `#!/bin/sh
echo "useradd $*" >> "$CITASK_TEST_CALL_LOG"
echo '_ci:*:499:499:ci:0:0:CI runner:/var/ci:/sbin/nologin' >> "$DESTDIR/etc/master.passwd"
`)
	return fixture
}

func (fixture *installFixture) inRoot(relativePath string) string {
	return filepath.Join(fixture.root, relativePath)
}

func (fixture *installFixture) runInstall(arguments ...string) (string, error) {
	command := exec.Command("sh", append([]string{"install-on-openbsd.sh"}, arguments...)...)
	command.Env = append(os.Environ(),
		"PATH="+fixture.stubDirectory+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DESTDIR="+fixture.root,
		"CITASK_TEST_CALL_LOG="+fixture.callLog,
	)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (fixture *installFixture) mustInstall(arguments ...string) string {
	fixture.t.Helper()
	output, err := fixture.runInstall(arguments...)
	if err != nil {
		fixture.t.Fatalf("install failed: %v\n%s", err, output)
	}
	return output
}

func (fixture *installFixture) standardArguments() []string {
	return []string{"--program", fixture.programSource, "--config", fixture.configurationSource}
}

func (fixture *installFixture) scheduleArguments() []string {
	return append(fixture.standardArguments(), "--start-schedule", "--mail-to", "someone")
}

func (fixture *installFixture) writeToken() {
	fixture.t.Helper()
	writeFileInFixture(fixture.t, fixture.inRoot("var/ci/etc/github-token"), fakeTokenContent, 0o600)
}

func (fixture *installFixture) readFile(relativePath string) string {
	fixture.t.Helper()
	content, err := os.ReadFile(fixture.inRoot(relativePath))
	if err != nil {
		fixture.t.Fatal(err)
	}
	return string(content)
}

func (fixture *installFixture) calls(prefix string) int {
	fixture.t.Helper()
	content, err := os.ReadFile(fixture.callLog)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func (fixture *installFixture) snapshot() map[string]string {
	fixture.t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(fixture.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		information, err := entry.Info()
		if err != nil {
			return err
		}
		fingerprint := fmt.Sprintf("%s %s", information.Mode(), information.ModTime())
		if !entry.IsDir() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fingerprint += fmt.Sprintf(" %x", sha256.Sum256(content))
		}
		snapshot[path] = fingerprint
		return nil
	})
	if err != nil {
		fixture.t.Fatal(err)
	}
	return snapshot
}

func assertInstallLeftEverythingUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	for path, fingerprint := range after {
		if before[path] != fingerprint {
			t.Errorf("%s changed:\n before %s\n after  %s", path, before[path], fingerprint)
		}
	}
	for path := range before {
		if _, stillThere := after[path]; !stillThere {
			t.Errorf("%s was removed", path)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	information, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := information.Mode().Perm(); got != want {
		t.Errorf("%s has mode %o, want %o", path, got, want)
	}
}

func assertOutputContains(t *testing.T, output, expected string) {
	t.Helper()
	if !strings.Contains(output, expected) {
		t.Errorf("expected %q in:\n%s", expected, output)
	}
}

func TestFirstInstallCreatesEverything(t *testing.T) {
	fixture := newInstallFixture(t)

	fixture.mustInstall(fixture.standardArguments()...)

	if got := fixture.readFile("etc/login.conf.d/ci"); got != "ci:datasize=4096M:coredumpsize=0:maxproc=256:tc=default:\n" {
		t.Errorf("login class: %q", got)
	}
	assertMode(t, fixture.inRoot("etc/login.conf.d/ci"), 0o644)
	if fixture.calls("useradd -u 499 -d /var/ci -s /sbin/nologin -L ci") != 1 {
		t.Errorf("the account should be created once, calls: %s", fixture.callLogText())
	}
	assertMode(t, fixture.inRoot("var/ci"), 0o755)
	assertMode(t, fixture.inRoot("var/ci/etc"), 0o700)
	assertMode(t, fixture.inRoot("usr/local/libexec/citask"), 0o755)
	assertMode(t, fixture.inRoot("usr/local/libexec/citask/citask"), 0o755)
	assertMode(t, fixture.inRoot("usr/local/libexec/citask/citask.json"), 0o644)
	if got := fixture.readFile("usr/local/libexec/citask/citask"); got != fakeProgram {
		t.Errorf("program: %q", got)
	}
	if got := fixture.readFile("usr/local/libexec/citask/citask.json"); got != validConfigurationContent {
		t.Errorf("configuration: %q", got)
	}
	for _, denyFile := range []string{"var/cron/cron.deny", "var/cron/at.deny"} {
		if got := fixture.readFile(denyFile); got != "_ci\n" {
			t.Errorf("%s: %q", denyFile, got)
		}
	}
	for _, expectedOwnership := range []string{"chown _ci:_ci", "chown root:wheel"} {
		if fixture.calls(expectedOwnership) == 0 {
			t.Errorf("expected %q in: %s", expectedOwnership, fixture.callLogText())
		}
	}
}

func (fixture *installFixture) callLogText() string {
	content, _ := os.ReadFile(fixture.callLog)
	return string(content)
}

func TestSecondInstallChangesNothingAndSaysSo(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.mustInstall(fixture.standardArguments()...)
	before := fixture.snapshot()

	output := fixture.mustInstall(fixture.standardArguments()...)

	assertInstallLeftEverythingUnchanged(t, before, fixture.snapshot())
	assertOutputContains(t, output, "0 changes")
	if got := fixture.calls("useradd"); got != 1 {
		t.Errorf("the account was created %d times", got)
	}
}

func TestSecondInstallWithTheScheduleChangesNothing(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.writeToken()
	fixture.mustInstall(fixture.scheduleArguments()...)
	before := fixture.snapshot()

	output := fixture.mustInstall(fixture.scheduleArguments()...)

	assertInstallLeftEverythingUnchanged(t, before, fixture.snapshot())
	assertOutputContains(t, output, "0 changes")
	if got := strings.Count(fixture.readFile("etc/crontab"), scheduleEntry); got != 1 {
		t.Errorf("schedule entry appears %d times", got)
	}
}

func TestInstallFinishesWhatAnEarlierPartialInstallLeft(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.mustInstall(fixture.standardArguments()...)
	for _, removedPath := range []string{"usr/local/libexec/citask/citask.json", "var/ci/etc"} {
		if err := os.RemoveAll(fixture.inRoot(removedPath)); err != nil {
			t.Fatal(err)
		}
	}
	writeFileInFixture(t, fixture.inRoot("var/cron/at.deny"), "", 0o660)

	fixture.mustInstall(fixture.standardArguments()...)

	if got := fixture.readFile("usr/local/libexec/citask/citask.json"); got != validConfigurationContent {
		t.Errorf("configuration: %q", got)
	}
	assertMode(t, fixture.inRoot("var/ci/etc"), 0o700)
	if got := fixture.readFile("var/cron/at.deny"); got != "_ci\n" {
		t.Errorf("at.deny: %q", got)
	}
	if got := fixture.calls("useradd"); got != 1 {
		t.Errorf("the account was created %d times", got)
	}
}

func TestInstallOnAMachineSetUpByHandOnlyAddsWhatIsMissing(t *testing.T) {
	fixture := newInstallFixture(t)
	writeFileInFixture(t, fixture.inRoot("etc/login.conf.d/ci"), "ci:datasize=4096M:coredumpsize=0:maxproc=256:tc=default:\n", 0o644)
	passwordFile := fixture.readFile("etc/master.passwd") + "_ci:*:499:499:ci:0:0:CI runner:/var/ci:/sbin/nologin\n"
	writeFileInFixture(t, fixture.inRoot("etc/master.passwd"), passwordFile, 0o600)

	fixture.mustInstall(fixture.standardArguments()...)

	if got := fixture.calls("useradd"); got != 0 {
		t.Errorf("an existing account must not be created again, got %d calls", got)
	}
}

func TestInstallKeepsAnExistingDenyListAndDoesNotRepeatTheEntry(t *testing.T) {
	fixture := newInstallFixture(t)
	writeFileInFixture(t, fixture.inRoot("var/cron/cron.deny"), "other-user\n_ci\n", 0o660)

	fixture.mustInstall(fixture.standardArguments()...)

	if got := fixture.readFile("var/cron/cron.deny"); got != "other-user\n_ci\n" {
		t.Errorf("cron.deny: %q", got)
	}
}

func TestInstallCreatesAMissingDenyFile(t *testing.T) {
	fixture := newInstallFixture(t)
	if err := os.Remove(fixture.inRoot("var/cron/at.deny")); err != nil {
		t.Fatal(err)
	}

	fixture.mustInstall(fixture.standardArguments()...)

	if got := fixture.readFile("var/cron/at.deny"); got != "_ci\n" {
		t.Errorf("at.deny: %q", got)
	}
	if fixture.calls("chown root:crontab") == 0 {
		t.Errorf("a new deny file should belong to crontab: %s", fixture.callLogText())
	}
}

func TestScheduleIsNotStartedUnlessAskedFor(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.writeToken()

	output := fixture.mustInstall(fixture.standardArguments()...)

	if _, err := os.Stat(fixture.inRoot("etc/crontab")); !os.IsNotExist(err) {
		t.Errorf("the schedule must not be started without --start-schedule: %v", err)
	}
	assertOutputContains(t, output, "not started")
}

func TestStartingTheScheduleCreatesTheSystemCrontab(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.writeToken()

	fixture.mustInstall(fixture.scheduleArguments()...)

	if got := fixture.readFile("etc/crontab"); got != "MAILTO=someone\n"+scheduleEntry+"\n" {
		t.Errorf("crontab: %q", got)
	}
	assertMode(t, fixture.inRoot("etc/crontab"), 0o644)
}

func TestStartingTheScheduleKeepsWhatIsAlreadyInTheSystemCrontab(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.writeToken()
	existingEntry := "0 3 * * * root /usr/local/bin/existing-job\n"
	writeFileInFixture(t, fixture.inRoot("etc/crontab"), existingEntry, 0o644)

	fixture.mustInstall(fixture.scheduleArguments()...)

	if got := fixture.readFile("etc/crontab"); got != existingEntry+"MAILTO=someone\n"+scheduleEntry+"\n" {
		t.Errorf("crontab: %q", got)
	}
}

func TestStartingTheScheduleIsRefusedWithoutATokenBeforeChangingAnything(t *testing.T) {
	fixture := newInstallFixture(t)
	before := fixture.snapshot()

	output, err := fixture.runInstall(fixture.scheduleArguments()...)

	if err == nil {
		t.Fatal("expected a refusal")
	}
	assertOutputContains(t, output, "token")
	assertInstallLeftEverythingUnchanged(t, before, fixture.snapshot())
}

func TestStartingTheScheduleIsRefusedWithoutAMailRecipient(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.writeToken()

	output, err := fixture.runInstall(append(fixture.standardArguments(), "--start-schedule")...)

	if err == nil {
		t.Fatal("expected a refusal")
	}
	assertOutputContains(t, output, "--mail-to")
}

func TestMailRecipientThatWouldBreakTheCrontabIsRefused(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.writeToken()
	for _, unsafeRecipient := range []string{"", "two words", "someone\n* * * * * root reboot", "$(reboot)"} {
		output, err := fixture.runInstall(append(fixture.standardArguments(), "--start-schedule", "--mail-to", unsafeRecipient)...)
		if err == nil {
			t.Errorf("%q should be refused:\n%s", unsafeRecipient, output)
		}
	}
	if _, err := os.Stat(fixture.inRoot("etc/crontab")); !os.IsNotExist(err) {
		t.Errorf("the crontab must not be written: %v", err)
	}
}

func TestProgramThatDiffersIsReplacedWhileTheOldOneStaysIntactForAnyRunningPoll(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.mustInstall(fixture.standardArguments()...)
	installedProgram := fixture.inRoot("usr/local/libexec/citask/citask")
	hardLinkToTheRunningProgram := filepath.Join(t.TempDir(), "running")
	if err := os.Link(installedProgram, hardLinkToTheRunningProgram); err != nil {
		t.Skipf("hard links are not available across these folders: %v", err)
	}
	newProgram := fakeProgram + "# a newer build\n"
	writeExecutable(t, fixture.programSource, newProgram)

	output := fixture.mustInstall(fixture.standardArguments()...)

	if got := fixture.readFile("usr/local/libexec/citask/citask"); got != newProgram {
		t.Errorf("program was not replaced: %q", got)
	}
	runningProgram, err := os.ReadFile(hardLinkToTheRunningProgram)
	if err != nil {
		t.Fatal(err)
	}
	if string(runningProgram) != fakeProgram {
		t.Error("the program was written into rather than replaced")
	}
	assertOutputContains(t, output, "1 change")
}

func TestLeftoverPartialFilesAreNotLeftBehind(t *testing.T) {
	fixture := newInstallFixture(t)

	fixture.mustInstall(fixture.standardArguments()...)

	entries, err := os.ReadDir(fixture.inRoot("usr/local/libexec/citask"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("expected only the program and config, got %v", entries)
	}
}

func TestInstalledConfigurationIsKeptEvenWhenTheGivenFileDiffers(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.mustInstall(fixture.standardArguments()...)
	editedConfiguration := "{\"Example\": \"edited on the machine to enrol another project\"}\n"
	writeFileInFixture(t, fixture.inRoot("usr/local/libexec/citask/citask.json"), editedConfiguration, 0o644)

	output := fixture.mustInstall(fixture.standardArguments()...)

	if got := fixture.readFile("usr/local/libexec/citask/citask.json"); got != editedConfiguration {
		t.Errorf("an edited configuration was overwritten: %q", got)
	}
	assertOutputContains(t, output, "differs")
}

func TestExistingLoginClassIsKept(t *testing.T) {
	fixture := newInstallFixture(t)
	editedLoginClass := "ci:datasize=8192M:coredumpsize=0:maxproc=256:tc=default:\n"
	writeFileInFixture(t, fixture.inRoot("etc/login.conf.d/ci"), editedLoginClass, 0o644)

	fixture.mustInstall(fixture.standardArguments()...)

	if got := fixture.readFile("etc/login.conf.d/ci"); got != editedLoginClass {
		t.Errorf("an edited login class was overwritten: %q", got)
	}
}

func TestProgramCanBeUpgradedWithoutGivingAConfiguration(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.mustInstall(fixture.standardArguments()...)

	output := fixture.mustInstall("--program", fixture.programSource)

	assertOutputContains(t, output, "0 changes")
}

func TestFirstInstallWithoutAConfigurationIsRefusedBeforeChangingAnything(t *testing.T) {
	fixture := newInstallFixture(t)
	before := fixture.snapshot()

	output, err := fixture.runInstall("--program", fixture.programSource)

	if err == nil {
		t.Fatal("expected a refusal")
	}
	assertOutputContains(t, output, "--config")
	assertInstallLeftEverythingUnchanged(t, before, fixture.snapshot())
}

func TestInvalidConfigurationIsRefusedAndNotInstalled(t *testing.T) {
	fixture := newInstallFixture(t)
	writeFileInFixture(t, fixture.configurationSource, "{\"Example\": \"invalid\"}\n", 0o644)

	output, err := fixture.runInstall(fixture.standardArguments()...)

	if err == nil {
		t.Fatal("expected a refusal")
	}
	assertOutputContains(t, output, "ExampleField")
	if _, statError := os.Stat(fixture.inRoot("usr/local/libexec/citask/citask.json")); !os.IsNotExist(statError) {
		t.Errorf("an invalid configuration was installed: %v", statError)
	}
}

func TestInstalledConfigurationThatTheProgramRejectsIsReported(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.mustInstall(fixture.standardArguments()...)
	writeFileInFixture(t, fixture.inRoot("usr/local/libexec/citask/citask.json"), "{\"Example\": \"invalid\"}\n", 0o644)

	output, err := fixture.runInstall("--program", fixture.programSource)

	if err == nil {
		t.Fatal("expected a refusal, because cron would fail on every poll")
	}
	assertOutputContains(t, output, "ExampleField")
}

func TestMissingProgramIsRefusedBeforeChangingAnything(t *testing.T) {
	fixture := newInstallFixture(t)
	before := fixture.snapshot()

	output, err := fixture.runInstall("--program", filepath.Join(t.TempDir(), "not-built-yet"), "--config", fixture.configurationSource)

	if err == nil {
		t.Fatal("expected a refusal")
	}
	assertOutputContains(t, output, "make build")
	assertInstallLeftEverythingUnchanged(t, before, fixture.snapshot())
}

func TestExistingAccountThatDiffersIsRefusedRatherThanChanged(t *testing.T) {
	accounts := map[string]struct {
		passwordLine string
		groupFile    string
		expected     string
	}{
		"wrong shell": {
			passwordLine: "_ci:*:499:499:ci:0:0:CI runner:/var/ci:/bin/ksh\n",
			groupFile:    "wheel:*:0:root,someone\n",
			expected:     "shell",
		},
		"wrong home": {
			passwordLine: "_ci:*:499:499:ci:0:0:CI runner:/home/_ci:/sbin/nologin\n",
			groupFile:    "wheel:*:0:root,someone\n",
			expected:     "home",
		},
		"wrong class": {
			passwordLine: "_ci:*:499:499:default:0:0:CI runner:/var/ci:/sbin/nologin\n",
			groupFile:    "wheel:*:0:root,someone\n",
			expected:     "class",
		},
		"in wheel": {
			passwordLine: "_ci:*:499:499:ci:0:0:CI runner:/var/ci:/sbin/nologin\n",
			groupFile:    "wheel:*:0:root,_ci,someone\n",
			expected:     "wheel",
		},
	}
	for accountName, account := range accounts {
		t.Run(accountName, func(t *testing.T) {
			fixture := newInstallFixture(t)
			writeFileInFixture(t, fixture.inRoot("etc/master.passwd"), fixture.readFile("etc/master.passwd")+account.passwordLine, 0o600)
			writeFileInFixture(t, fixture.inRoot("etc/group"), account.groupFile, 0o644)
			before := fixture.snapshot()

			output, err := fixture.runInstall(fixture.standardArguments()...)

			if err == nil {
				t.Fatalf("expected a refusal:\n%s", output)
			}
			assertOutputContains(t, output, account.expected)
			assertInstallLeftEverythingUnchanged(t, before, fixture.snapshot())
		})
	}
}

func TestInstallIsRefusedWhenNotRunAsRoot(t *testing.T) {
	fixture := newInstallFixture(t)
	writeExecutable(t, filepath.Join(fixture.stubDirectory, "id"), "#!/bin/sh\necho 1000\n")
	before := fixture.snapshot()

	output, err := fixture.runInstall(fixture.standardArguments()...)

	if err == nil {
		t.Fatal("expected a refusal")
	}
	assertOutputContains(t, output, "root")
	assertInstallLeftEverythingUnchanged(t, before, fixture.snapshot())
}

func TestTokenIsNeitherChangedNorPrinted(t *testing.T) {
	fixture := newInstallFixture(t)
	fixture.writeToken()

	output := fixture.mustInstall(fixture.scheduleArguments()...)

	if got := fixture.readFile("var/ci/etc/github-token"); got != fakeTokenContent {
		t.Errorf("token: %q", got)
	}
	assertMode(t, fixture.inRoot("var/ci/etc/github-token"), 0o600)
	if strings.Contains(output, strings.TrimSpace(fakeTokenContent)) {
		t.Errorf("the token was printed:\n%s", output)
	}
}

func TestUnknownArgumentIsRefused(t *testing.T) {
	fixture := newInstallFixture(t)

	output, err := fixture.runInstall("--unknown")

	if err == nil {
		t.Fatal("expected a refusal")
	}
	assertOutputContains(t, output, "usage")
}
