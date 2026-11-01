package application_test

import (
	"bufio"
	"encoding/json"
	"errors"
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

func TestNewTrunkCommitRunsOnceAndAnUnchangedCommitRunsNothingAndPostsNothing(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	project.commitStepsOnTrunk(shellStep("record-run", harness.scriptRecordingStepRan("record-run")))

	harness.poll()
	assertEqual(t, "runs after the first poll", harness.stepRunCount("record-run"), 1)
	requestCountAfterFirstPoll := harness.github.requestCount()

	harness.poll()
	assertEqual(t, "runs after the second poll", harness.stepRunCount("record-run"), 1)
	assertEqual(t, "requests after the second poll", harness.github.requestCount(), requestCountAfterFirstPoll)
}

func TestTrunkOtherThanMasterIsFollowedAndItsMasterBranchIsIgnored(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "develop")
	trunkCommit := project.commitStepsOnTrunk(shellStep("record-run", harness.scriptRecordingStepRan("record-run")))

	harness.poll()
	assertEqual(t, "tested commit", project.lastTestedCommit(), trunkCommit)
	assertEqual(t, "runs", harness.stepRunCount("record-run"), 1)

	project.commitFilesOnBranch("master", map[string]string{"on-master.txt": "not the trunk"})
	harness.poll()
	assertEqual(t, "runs after a commit on master", harness.stepRunCount("record-run"), 1)
	assertEqual(t, "tested commit after a commit on master", project.lastTestedCommit(), trunkCommit)
}

func TestProjectWithNoLastTestedCommitTestsItsCurrentTrunkCommit(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	currentCommit := project.commitPassingStepOnTrunk()
	assertEqual(t, "tested commit before the first poll", project.lastTestedCommit(), "")

	harness.poll()

	assertEqual(t, "tested commit", project.lastTestedCommit(), currentCommit)
}

func TestSeveralNewCommitsTestOnlyTheNewestAndOtherBranchesRunNothing(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	recordingStep := shellStep("record-run", harness.scriptRecordingStepRan("record-run"))
	olderCommit := project.commitStepsOnTrunk(recordingStep)
	project.commitFilesOnBranch("master", map[string]string{"second.txt": "second"})
	newestCommit := project.commitFilesOnBranch("master", map[string]string{"third.txt": "third"})

	harness.poll()

	assertEqual(t, "runs", harness.stepRunCount("record-run"), 1)
	assertEqual(t, "tested commit", project.lastTestedCommit(), newestCommit)
	assertEqual(t, "statuses on the older commit", len(project.commitStatusStatesFor(olderCommit)), 0)

	project.commitFilesOnBranch("feature/other-branch", map[string]string{"feature.txt": "feature"})
	harness.poll()

	assertEqual(t, "runs after a commit on another branch", harness.stepRunCount("record-run"), 1)
	assertEqual(t, "tested commit after a commit on another branch", project.lastTestedCommit(), newestCommit)
}

func TestGitFailureStartsNoRunRecordsTheProblemAndLeavesTheLastTestedCommitUnchanged(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	recordingStep := shellStep("record-run", harness.scriptRecordingStepRan("record-run"))
	firstCommit := project.commitStepsOnTrunk(recordingStep)
	harness.poll()
	secondCommit := project.commitFilesOnBranch("master", map[string]string{"second.txt": "second"})
	requestCountBeforeFailure := harness.github.requestCount()

	unreachableDirectory := project.bareRepositoryDirectory + ".unreachable"
	if err := os.Rename(project.bareRepositoryDirectory, unreachableDirectory); err != nil {
		t.Fatal(err)
	}
	harness.poll()

	assertEqual(t, "runs during the failure", harness.stepRunCount("record-run"), 1)
	assertEqual(t, "tested commit during the failure", project.lastTestedCommit(), firstCommit)
	assertEqual(t, "requests during the failure", harness.github.requestCount(), requestCountBeforeFailure)
	if project.textOfPollProblemsLog() == "" {
		t.Error("the git failure should be recorded in poll-problems.log")
	}

	if err := os.Rename(unreachableDirectory, project.bareRepositoryDirectory); err != nil {
		t.Fatal(err)
	}
	harness.poll()

	assertEqual(t, "runs after recovery", harness.stepRunCount("record-run"), 2)
	assertEqual(t, "tested commit after recovery", project.lastTestedCommit(), secondCommit)
}

func TestGitHubThatNeverRespondsCannotHangAPollAndTheResultIsKeptForReplay(t *testing.T) {
	harness := newPollHarness(t)
	harness.gitHubRequestTimeLimit = 200 * time.Millisecond
	project := harness.enrolProject("project-one", "master")
	commitSha := project.commitStepsOnTrunk(shellStep("record-run", harness.scriptRecordingStepRan("record-run")))
	harness.github.setBehaviour(neverRespond)

	pollStartedAt := time.Now()
	harness.poll()

	if elapsed := time.Since(pollStartedAt); elapsed > generousBoundForAPoll {
		t.Errorf("a GitHub that never responds held the poll for %s", elapsed)
	}
	assertEqual(t, "runs", harness.stepRunCount("record-run"), 1)
	assertEqual(t, "tested commit", project.lastTestedCommit(), commitSha)
	assertEqual(t, "results kept for replay", project.unreportedResultCount(), 1)

	harness.github.setBehaviour(respondNormally)
	harness.gitHubRequestTimeLimit = 30 * time.Second
	harness.poll()

	assertEqual(t, "runs after replay", harness.stepRunCount("record-run"), 1)
	assertEqual(t, "results kept after replay", project.unreportedResultCount(), 0)
	assertStringSlicesEqual(t, "statuses after replay", project.commitStatusStatesFor(commitSha), []string{"success"})
}

func TestHeldLockExitsWithoutRunningAndAFreedLockRuns(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	project.commitStepsOnTrunk(shellStep("record-run", harness.scriptRecordingStepRan("record-run")))
	releaseLock := harness.holdExclusivePollLock()

	harness.poll()

	assertEqual(t, "runs while the lock is held", harness.stepRunCount("record-run"), 0)
	assertEqual(t, "requests while the lock is held", harness.github.requestCount(), 0)

	releaseLock()
	harness.poll()

	assertEqual(t, "runs after the lock is freed", harness.stepRunCount("record-run"), 1)
}

func TestTwoProjectsRunOneAfterAnother(t *testing.T) {
	harness := newPollHarness(t)
	firstProject := harness.enrolProject("project-one", "master")
	secondProject := harness.enrolProject("project-two", "master")
	firstProject.commitStepsOnTrunk(shellStep("first", fmt.Sprintf(
		`touch %q; sleep 0.3; if [ -e %q ]; then touch %q; fi; touch %q`,
		harness.markerPath("first-started"),
		harness.markerPath("second-started"),
		harness.markerPath("runs-overlapped"),
		harness.markerPath("first-finished"),
	)))
	secondProject.commitStepsOnTrunk(shellStep("second", fmt.Sprintf(
		`touch %q; if [ ! -e %q ]; then touch %q; fi`,
		harness.markerPath("second-started"),
		harness.markerPath("first-finished"),
		harness.markerPath("runs-overlapped"),
	)))

	harness.poll()

	assertEqual(t, "first project ran", harness.markerExists("first-finished"), true)
	assertEqual(t, "second project ran", harness.markerExists("second-started"), true)
	assertEqual(t, "runs overlapped", harness.markerExists("runs-overlapped"), false)
}

const lockPathForHelperProcess = "CITASK_TEST_LOCK_PATH_FOR_HELPER_PROCESS"

func TestHelperProcessThatHoldsThePollLockUntilItIsKilled(t *testing.T) {
	lockPath := os.Getenv(lockPathForHelperProcess)
	if lockPath == "" {
		t.Skip("this only does something when started as a helper process")
	}
	lockFile := openExclusivePollLockFile(t, lockPath)
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	fmt.Println("lock-held")
	time.Sleep(5 * time.Minute)
}

func TestProcessKilledWhileHoldingTheLockReleasesItAndTheNextPollRuns(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	project.commitStepsOnTrunk(shellStep("record-run", harness.scriptRecordingStepRan("record-run")))

	helperProcess := exec.Command(os.Args[0], "-test.run=^TestHelperProcessThatHoldsThePollLockUntilItIsKilled$")
	helperProcess.Env = append(os.Environ(), lockPathForHelperProcess+"="+harness.exclusivePollLockFile())
	helperOutput, err := helperProcess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helperProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { helperProcess.Process.Kill() })
	firstLineFromHelper, err := bufio.NewReader(helperOutput).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "helper output", strings.TrimSpace(firstLineFromHelper), "lock-held")

	harness.poll()
	assertEqual(t, "runs while the helper holds the lock", harness.stepRunCount("record-run"), 0)

	if err := helperProcess.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	helperProcess.Wait()
	harness.poll()

	assertEqual(t, "runs after the holder was killed", harness.stepRunCount("record-run"), 1)
}

func hangingStepIgnoringTerminate(name, childProcessIdentifierFile string) stepFixture {
	return shellStep(name, fmt.Sprintf(`trap '' TERM; sleep 60 & echo $! > %q; wait`, childProcessIdentifierFile))
}

func processIsGone(processIdentifier int) bool {
	return errors.Is(syscall.Kill(processIdentifier, 0), syscall.ESRCH)
}

func assertProcessStops(t *testing.T, processIdentifier int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !processIsGone(processIdentifier) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still running", processIdentifier)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHangingStepIsStoppedReportedAsFailedAndLaterStepsAreSkipped(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	project.definition.RunTimeLimit = shortRunTimeLimit
	commitSha := project.commitStepsOnTrunk(
		hangingStepIgnoringTerminate("hanging-step", harness.markerPath("hanging-child")),
		shellStep("later-step", harness.scriptRecordingStepRan("later-step")),
	)

	pollStartedAt := time.Now()
	harness.poll()

	if elapsed := time.Since(pollStartedAt); elapsed > generousBoundForAPoll {
		t.Errorf("the run was not stopped at its limit, it took %s", elapsed)
	}
	assertEqual(t, "later step runs", harness.stepRunCount("later-step"), 0)
	assertStringSlicesEqual(t, "statuses", project.commitStatusStatesFor(commitSha), []string{"pending", "failure"})
	issue := project.onlyFailureIssue()
	assertContains(t, "issue body", issue.Body, "hanging-step")
	assertContains(t, "issue body", issue.Body, "stopped-by-run-time-limit")
	assertContains(t, "run log", project.textOfAllRunLogs(), "skipped")
	assertEqual(t, "tested commit", project.lastTestedCommit(), commitSha)

	childProcessIdentifierText, err := os.ReadFile(harness.markerPath("hanging-child"))
	if err != nil {
		t.Fatal(err)
	}
	childProcessIdentifier, err := strconv.Atoi(strings.TrimSpace(string(childProcessIdentifierText)))
	if err != nil {
		t.Fatal(err)
	}
	assertProcessStops(t, childProcessIdentifier)
}

func TestUntrackedAndIgnoredFilesFromAPreviousRunAreGone(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	project.commitFilesOnBranch("master", map[string]string{
		".gitignore": "*.ignored\n",
		"ci-steps.json": stepDefinitionFileContent(t, shellStep("leave-files-behind",
			"echo left > untracked-file && echo left > build-output.ignored && mkdir -p ignored-folder.ignored && echo left > ignored-folder.ignored/file")),
	})
	harness.poll()
	assertPathExists(t, filepath.Join(project.cloneDirectory(), "untracked-file"))
	assertPathExists(t, filepath.Join(project.cloneDirectory(), "build-output.ignored"))

	commitSha := project.commitStepsOnTrunk(shellStep("check-files-are-gone",
		"test ! -e untracked-file && test ! -e build-output.ignored && test ! -e ignored-folder.ignored"))
	harness.poll()

	assertStringSlicesEqual(t, "statuses", project.commitStatusStatesFor(commitSha), []string{"pending", "success"})
}

func TestCheckoutIsTheExactCommitUnderTest(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	commitSha := project.commitStepsOnTrunk(shellStep("record-commit",
		`git rev-parse HEAD > "$CI_RUN_ARTIFACT_DIRECTORY/commit-under-test.txt"`))

	harness.poll()

	assertEqual(t, "commit seen by the step", project.textOfArtifactFromLatestRun("commit-under-test.txt"), commitSha)
}

func TestLeftoverProcessesAreStoppedAfterEveryRunWhateverItsOutcome(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	project.definition.RunTimeLimit = shortRunTimeLimit

	project.commitPassingStepOnTrunk()
	harness.poll()
	assertEqual(t, "stops after a pass", harness.leftoverProcessStopper.callCount, 1)

	project.commitFailingStepOnTrunk()
	harness.poll()
	assertEqual(t, "stops after a failure", harness.leftoverProcessStopper.callCount, 2)

	project.commitStepsOnTrunk(shellStep("hanging-step", "sleep 60"))
	harness.poll()
	assertEqual(t, "stops after the time limit", harness.leftoverProcessStopper.callCount, 3)

	project.commitFilesOnBranch("master", map[string]string{"ci-steps.json": "not json"})
	harness.poll()
	assertEqual(t, "stops after invalid step definitions", harness.leftoverProcessStopper.callCount, 4)

	for callIndex, privateTemporaryFolderExisted := range harness.leftoverProcessStopper.privateTemporaryFolderExistedDuring {
		assertEqual(t, fmt.Sprintf("private folder still existed during stop %d", callIndex), privateTemporaryFolderExisted, true)
	}
}

func TestPrivateTemporaryFolderExistsDuringARunAndIsDeletedAfterwards(t *testing.T) {
	scriptRecordingPrivateFolder := `test -d "$TMPDIR" && test -d "$XDG_RUNTIME_DIR" && touch "$TMPDIR/left-behind" && echo "$TMPDIR" > "$CI_RUN_ARTIFACT_DIRECTORY/private-folder.txt"`
	outcomes := []struct {
		name        string
		finalScript string
	}{
		{name: "after a pass", finalScript: "exit 0"},
		{name: "after a failure", finalScript: "exit 1"},
		{name: "after the time limit", finalScript: "sleep 60"},
	}
	for _, outcome := range outcomes {
		t.Run(outcome.name, func(t *testing.T) {
			harness := newPollHarness(t)
			project := harness.enrolProject("project-one", "master")
			project.definition.RunTimeLimit = shortRunTimeLimit
			neighbouringFile := filepath.Join(harness.temporaryFoldersParentDirectory, "neighbouring-file")
			if err := os.WriteFile(neighbouringFile, []byte("not citask's"), 0o644); err != nil {
				t.Fatal(err)
			}
			commitSha := project.commitStepsOnTrunk(shellStep("use-private-folder", scriptRecordingPrivateFolder+" && "+outcome.finalScript))

			harness.poll()

			privateFolder := project.textOfArtifactFromLatestRun("private-folder.txt")
			expectedPrefix := filepath.Join(harness.temporaryFoldersParentDirectory, "citask-project-one-"+commitSha[:7]+"-")
			if !strings.HasPrefix(privateFolder, expectedPrefix) {
				t.Errorf("private folder %q should start with %q", privateFolder, expectedPrefix)
			}
			assertPathIsGone(t, privateFolder)
			assertPathExists(t, neighbouringFile)
			remainingEntries, err := os.ReadDir(harness.temporaryFoldersParentDirectory)
			if err != nil {
				t.Fatal(err)
			}
			assertEqual(t, "entries left in the parent folder", len(remainingEntries), 1)
		})
	}
}

func TestMissingOrInvalidStepDefinitionsFailTheRunAndNameTheProblem(t *testing.T) {
	invalidFiles := []struct {
		name                string
		files               map[string]string
		expectedInTheRunLog string
	}{
		{
			name:                "missing file",
			files:               map[string]string{"README.md": "no step definitions here"},
			expectedInTheRunLog: "ci-steps.json",
		},
		{
			name:                "not JSON",
			files:               map[string]string{"ci-steps.json": "{ not json"},
			expectedInTheRunLog: "ci-steps.json",
		},
		{
			name: "unknown field",
			files: map[string]string{"ci-steps.json": `{"Steps":[{"Name":"build","WorkingDirectory":".",` +
				`"Command":["true"],"DependOn":["misspelt"]}]}`},
			expectedInTheRunLog: "DependOn",
		},
	}
	for _, invalidFile := range invalidFiles {
		t.Run(invalidFile.name, func(t *testing.T) {
			harness := newPollHarness(t)
			project := harness.enrolProject("project-one", "master")
			commitSha := project.commitFilesOnBranch("master", invalidFile.files)

			harness.poll()

			assertStringSlicesEqual(t, "statuses", project.commitStatusStatesFor(commitSha), []string{"pending", "failure"})
			assertContains(t, "issue body", project.onlyFailureIssue().Body, "read-step-definitions")
			assertContains(t, "run log", project.textOfAllRunLogs(), invalidFile.expectedInTheRunLog)
			assertEqual(t, "tested commit", project.lastTestedCommit(), commitSha)
		})
	}
}

func TestFailedStepSkipsItsDependentsAndIsNamedWhileIndependentStepsStillRun(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	commitSha := project.commitStepsOnTrunk(
		shellStep("compile", "exit 1"),
		shellStep("package-output", harness.scriptRecordingStepRan("package-output")).dependingOn("compile"),
		shellStep("verify-package", harness.scriptRecordingStepRan("verify-package")).dependingOn("package-output"),
		shellStep("independent-check", harness.scriptRecordingStepRan("independent-check")),
	)

	harness.poll()

	assertEqual(t, "dependent step runs", harness.stepRunCount("package-output"), 0)
	assertEqual(t, "indirectly dependent step runs", harness.stepRunCount("verify-package"), 0)
	assertEqual(t, "independent step runs", harness.stepRunCount("independent-check"), 1)
	assertStringSlicesEqual(t, "statuses", project.commitStatusStatesFor(commitSha), []string{"pending", "failure"})
	issue := project.onlyFailureIssue()
	assertContains(t, "issue body", issue.Body, "compile")
	assertDoesNotContain(t, "issue body", issue.Body, "independent-check")
	assertContains(t, "run log", project.textOfAllRunLogs(), "package-output")
	assertContains(t, "run log", project.textOfAllRunLogs(), "skipped")
}

func TestStepThatFailsThenPassesReportsAPassAndKeepsTheFailureInTheRunLog(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	commitSha := project.commitStepsOnTrunk(shellStep("flaky-tests",
		`if [ -e "$TMPDIR/attempted" ]; then echo output-of-second-attempt; exit 0; fi; touch "$TMPDIR/attempted"; echo output-of-first-attempt; exit 1`,
	).repeatingOnceOnFailure())

	harness.poll()

	assertStringSlicesEqual(t, "statuses", project.commitStatusStatesFor(commitSha), []string{"pending", "success"})
	assertEqual(t, "failure issues", len(project.failureIssues()), 0)
	runLog := project.textOfAllRunLogs()
	assertContains(t, "run log", runLog, "output-of-first-attempt")
	assertContains(t, "run log", runLog, "output-of-second-attempt")
	assertContains(t, "run log", runLog, "passed-on-repeat")
}

func TestStepWithoutRepeatIsNotRepeated(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	project.commitStepsOnTrunk(shellStep("once-only", harness.scriptRecordingStepRan("once-only")+"; exit 1"))

	harness.poll()

	assertEqual(t, "attempts", harness.stepRunCount("once-only"), 1)
}

func TestCommitStatusIsPendingThenTheOutcomeWithTheRightCommitAndContext(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")

	passingCommit := project.commitPassingStepOnTrunk()
	harness.poll()
	assertStringSlicesEqual(t, "statuses of the passing commit", project.commitStatusStatesFor(passingCommit), []string{"pending", "success"})

	failingCommit := project.commitFailingStepOnTrunk()
	harness.poll()
	assertStringSlicesEqual(t, "statuses of the failing commit", project.commitStatusStatesFor(failingCommit), []string{"pending", "failure"})

	for _, status := range project.commitStatuses() {
		assertEqual(t, "status context", status.Context, commitStatusContext)
	}
}

func TestLongListOfFailedStepsStaysWithinTheCommitStatusDescriptionLimit(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	failingSteps := []stepFixture{}
	for stepNumber := range 12 {
		failingSteps = append(failingSteps, shellStep(fmt.Sprintf("a-rather-long-failing-step-name-%02d", stepNumber), "exit 1"))
	}
	commitSha := project.commitStepsOnTrunk(failingSteps...)

	harness.poll()

	assertStringSlicesEqual(t, "statuses", project.commitStatusStatesFor(commitSha), []string{"pending", "failure"})
	assertEqual(t, "results left unreported", project.unreportedResultCount(), 0)
}

func TestFailureIssueIsOpenedOnceThenCommentedThenClosedByAPass(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")

	firstFailingCommit := project.commitFailingStepOnTrunk()
	harness.poll()

	issue := project.onlyFailureIssue()
	assertEqual(t, "issue title", issue.Title, project.failureIssueTitle())
	assertEqual(t, "issue state", issue.State, "open")
	assertContains(t, "issue body", issue.Body, "project-one")
	assertContains(t, "issue body", issue.Body, firstFailingCommit)
	assertContains(t, "issue body", issue.Body, "failing-step")
	assertContains(t, "issue body", issue.Body, project.runIdentifiers()[0])

	secondFailingCommit := project.commitFailingStepOnTrunk()
	harness.poll()

	issue = project.onlyFailureIssue()
	assertEqual(t, "issue state after the second failure", issue.State, "open")
	assertEqual(t, "comments after the second failure", len(issue.Comments), 1)
	assertContains(t, "comment", issue.Comments[0], secondFailingCommit)

	passingCommit := project.commitPassingStepOnTrunk()
	harness.poll()

	issue = project.onlyFailureIssue()
	assertEqual(t, "issue state after the pass", issue.State, "closed")
	assertEqual(t, "issue close reason", issue.StateReason, "completed")
	assertEqual(t, "comments after the pass", len(issue.Comments), 2)
	assertContains(t, "closing comment", issue.Comments[1], passingCommit)
}

func TestPullRequestWithTheSameTitleIsNotTreatedAsTheFailureIssue(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	harness.github.addOpenPullRequest(testGitHubOwner, project.definition.GitHubRepository, project.failureIssueTitle())

	project.commitFailingStepOnTrunk()
	harness.poll()

	assertEqual(t, "issue state", project.onlyFailureIssue().State, "open")
}

func TestRejectedTokenStillRunsStepsAndResultsAreReplayedOldestFirst(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	harness.github.setBehaviour(rejectEveryRequestAsUnauthorized)

	failingCommit := project.commitStepsOnTrunk(shellStep("failing-step", harness.scriptRecordingStepRan("failing-step")+"; exit 1"))
	harness.poll()
	passingCommit := project.commitStepsOnTrunk(shellStep("passing-step", harness.scriptRecordingStepRan("passing-step")))
	harness.poll()

	assertEqual(t, "failing step runs", harness.stepRunCount("failing-step"), 1)
	assertEqual(t, "passing step runs", harness.stepRunCount("passing-step"), 1)
	assertEqual(t, "results waiting", project.unreportedResultCount(), 2)
	assertContains(t, "run log", project.textOfAllRunLogs(), "401")

	harness.github.setBehaviour(respondNormally)
	harness.poll()

	assertEqual(t, "results waiting after replay", project.unreportedResultCount(), 0)
	assertEqual(t, "failing step runs after replay", harness.stepRunCount("failing-step"), 1)
	statuses := project.commitStatuses()
	if len(statuses) != 2 {
		t.Fatalf("expected two replayed statuses, got %+v", statuses)
	}
	assertEqual(t, "oldest result first", statuses[0].CommitSha, failingCommit)
	assertEqual(t, "oldest result outcome", statuses[0].State, "failure")
	assertEqual(t, "newest result last", statuses[1].CommitSha, passingCommit)
	assertEqual(t, "newest result outcome", statuses[1].State, "success")
}

func TestMissingTokenStillRunsStepsAndTheResultIsReplayedOnceTheTokenReturns(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	commitSha := project.commitStepsOnTrunk(shellStep("record-run", harness.scriptRecordingStepRan("record-run")))
	temporaryTokenLocation := harness.gitHubTokenFile + ".moved"
	if err := os.Rename(harness.gitHubTokenFile, temporaryTokenLocation); err != nil {
		t.Fatal(err)
	}

	harness.poll()

	assertEqual(t, "runs", harness.stepRunCount("record-run"), 1)
	assertEqual(t, "results waiting", project.unreportedResultCount(), 1)
	assertContains(t, "run log", project.textOfAllRunLogs(), "token")

	if err := os.Rename(temporaryTokenLocation, harness.gitHubTokenFile); err != nil {
		t.Fatal(err)
	}
	harness.poll()

	assertEqual(t, "results waiting after replay", project.unreportedResultCount(), 0)
	assertStringSlicesEqual(t, "statuses", project.commitStatusStatesFor(commitSha), []string{"success"})
}

func TestNewResultIsNotPostedAheadOfAnOlderResultStillWaiting(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	harness.github.setBehaviour(failEveryRequestWithServerError)
	olderCommit := project.commitFailingStepOnTrunk()
	harness.poll()
	harness.github.setBehaviour(respondNormally)
	newerCommit := project.commitPassingStepOnTrunk()

	harness.poll()

	statuses := project.commitStatuses()
	finalStatuses := []recordedCommitStatus{}
	for _, status := range statuses {
		if status.State != "pending" {
			finalStatuses = append(finalStatuses, status)
		}
	}
	if len(finalStatuses) != 2 {
		t.Fatalf("expected two final statuses, got %+v", statuses)
	}
	assertEqual(t, "oldest result first", finalStatuses[0].CommitSha, olderCommit)
	assertEqual(t, "newest result last", finalStatuses[1].CommitSha, newerCommit)
}

func TestFailureAndLaterPassBothQueuedDuringAnOutageLeaveTheIssueClosed(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	harness.github.setBehaviour(failEveryRequestWithServerError)
	project.commitFailingStepOnTrunk()
	harness.poll()
	passingCommit := project.commitPassingStepOnTrunk()
	harness.poll()
	assertEqual(t, "results waiting", project.unreportedResultCount(), 2)

	harness.github.setBehaviour(respondNormally)
	harness.poll()

	issue := project.onlyFailureIssue()
	assertEqual(t, "issue state", issue.State, "closed")
	assertEqual(t, "closing comments", len(issue.Comments), 1)
	assertContains(t, "closing comment", issue.Comments[0], passingCommit)
}

func TestTokenAppearsInNoStepEnvironmentAndInNoRunLog(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", fakeGitHubToken)
	t.Setenv("CITASK_ENVIRONMENT_VARIABLE_OUTSIDE_THE_ALLOWLIST", "must-not-reach-a-step")
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	project.commitStepsOnTrunk(stepFixture{
		Name:             "print-environment",
		WorkingDirectory: ".",
		Command:          []string{"env"},
		Environment:      map[string]string{"STEP_DECLARED_VARIABLE": "declared-value"},
	})

	harness.poll()

	runLog := project.textOfAllRunLogs()
	assertDoesNotContain(t, "run log", runLog, fakeGitHubToken)
	assertDoesNotContain(t, "run log", runLog, "CITASK_ENVIRONMENT_VARIABLE_OUTSIDE_THE_ALLOWLIST")
	assertContains(t, "run log", runLog, "CI=true")
	assertContains(t, "run log", runLog, "STEP_DECLARED_VARIABLE=declared-value")

	allowedVariableNames := map[string]bool{
		"HOME": true, "PATH": true, "USER": true, "LOGNAME": true, "LANG": true, "LC_ALL": true, "CI": true,
		"TMPDIR": true, "XDG_RUNTIME_DIR": true, "CI_RUN_ARTIFACT_DIRECTORY": true, "STEP_DECLARED_VARIABLE": true,
	}
	for _, runLogLine := range strings.Split(runLog, "\n") {
		variableName, _, lineIsAnAssignment := strings.Cut(runLogLine, "=")
		if lineIsAnAssignment && variableName != "" && variableName == strings.ToUpper(variableName) && !allowedVariableNames[variableName] {
			t.Errorf("variable %q reached a step but is not on the allowlist", variableName)
		}
	}
}

func TestFailingStepOutputNeverReachesGitHubButStaysInTheRunLog(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	fakeSecret := "not-a-real-secret-value"
	fakePath := "/private/path/on/the/box"
	fakeHostname := "private-host.example.internal"
	project.commitStepsOnTrunk(shellStep("noisy-failure",
		fmt.Sprintf("echo %s; echo %s; echo %s; echo \"$HOME\"; pwd; exit 1", fakeSecret, fakePath, fakeHostname)))

	harness.poll()

	requests := harness.github.textOfEveryRequest()
	runLog := project.textOfAllRunLogs()
	for _, sensitiveValue := range []string{fakeSecret, fakePath, fakeHostname} {
		assertDoesNotContain(t, "requests to GitHub", requests, sensitiveValue)
		assertContains(t, "run log", runLog, sensitiveValue)
	}
	assertDoesNotContain(t, "requests to GitHub", requests, harness.ciHomeDirectory)
	assertDoesNotContain(t, "requests to GitHub", requests, harness.temporaryFoldersParentDirectory)
}

func writeOldRunRecords(t *testing.T, project *enrolledProject, runIdentifier string, finishedAt time.Time) {
	t.Helper()
	runFolder := filepath.Join(project.logsDirectory(), runIdentifier)
	if err := os.MkdirAll(runFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runFolder, "run.log"), []byte("old run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreportedResultsDirectory := filepath.Join(project.stateDirectory(), "unreported-results")
	if err := os.MkdirAll(unreportedResultsDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	unreportedResult, err := json.Marshal(map[string]any{
		"ProjectName":   project.definition.Name,
		"CommitSha":     strings.Repeat("a", 40),
		"RunIdentifier": runIdentifier,
		"Passed":        true,
		"FinishedAt":    finishedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unreportedResultsDirectory, runIdentifier+".json"), unreportedResult, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunRecordsPastRetentionAreDeletedAndNewerOnesAreKept(t *testing.T) {
	harness := newPollHarness(t)
	project := harness.enrolProject("project-one", "master")
	project.commitPassingStepOnTrunk()

	expiredFinishedAt := time.Now().UTC().AddDate(0, 0, -31)
	expiredRunIdentifier := expiredFinishedAt.Format("20060102T150405Z") + "-aaaaaaa"
	writeOldRunRecords(t, project, expiredRunIdentifier, expiredFinishedAt)

	recentRunIdentifier := time.Now().UTC().AddDate(0, 0, -29).Format("20060102T150405Z") + "-bbbbbbb"
	recentRunFolder := filepath.Join(project.logsDirectory(), recentRunIdentifier)
	if err := os.MkdirAll(recentRunFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	folderCitaskDidNotCreate := filepath.Join(project.logsDirectory(), "kept-by-someone-else")
	if err := os.MkdirAll(folderCitaskDidNotCreate, 0o755); err != nil {
		t.Fatal(err)
	}

	harness.poll()

	assertPathIsGone(t, filepath.Join(project.logsDirectory(), expiredRunIdentifier))
	assertPathIsGone(t, filepath.Join(project.stateDirectory(), "unreported-results", expiredRunIdentifier+".json"))
	assertPathExists(t, recentRunFolder)
	assertPathExists(t, folderCitaskDidNotCreate)
	assertEqual(t, "statuses for the expired result", len(project.commitStatusStatesFor(strings.Repeat("a", 40))), 0)
}

func TestSecondProjectWithItsOwnTrunkAndTimeLimitNeedsNoOtherChange(t *testing.T) {
	harness := newPollHarness(t)
	firstProject := harness.enrolProject("project-one", "master")
	secondProject := harness.enrolProject("project-two", "release/stable")
	secondProject.definition.RunTimeLimit = shortRunTimeLimit

	firstCommit := firstProject.commitPassingStepOnTrunk()
	secondCommit := secondProject.commitStepsOnTrunk(shellStep("hanging-step", "sleep 60"))
	harness.poll()

	assertStringSlicesEqual(t, "first project statuses", firstProject.commitStatusStatesFor(firstCommit), []string{"pending", "success"})
	assertStringSlicesEqual(t, "second project statuses", secondProject.commitStatusStatesFor(secondCommit), []string{"pending", "failure"})
	assertEqual(t, "second project issue title", secondProject.onlyFailureIssue().Title, "CI failing on release/stable (citask)")
	assertContains(t, "second project issue", secondProject.onlyFailureIssue().Body, "stopped-by-run-time-limit")
}

func TestProjectThatCannotBeCheckedDoesNotStopTheNextProject(t *testing.T) {
	harness := newPollHarness(t)
	brokenProject := harness.enrolProject("broken-project", "master")
	brokenProject.commitPassingStepOnTrunk()
	brokenProject.definition.CloneUrl = filepath.Join(t.TempDir(), "repository-that-does-not-exist.git")
	healthyProject := harness.enrolProject("healthy-project", "master")
	healthyCommit := healthyProject.commitPassingStepOnTrunk()

	harness.poll()

	assertEqual(t, "tested commit of the broken project", brokenProject.lastTestedCommit(), "")
	assertEqual(t, "tested commit of the healthy project", healthyProject.lastTestedCommit(), healthyCommit)
}

func TestProjectWhoseRunFailsDoesNotStopTheNextProject(t *testing.T) {
	harness := newPollHarness(t)
	failingProject := harness.enrolProject("failing-project", "master")
	failingProject.commitFailingStepOnTrunk()
	healthyProject := harness.enrolProject("healthy-project", "master")
	healthyCommit := healthyProject.commitPassingStepOnTrunk()

	harness.poll()

	assertEqual(t, "failing project issues", len(failingProject.failureIssues()), 1)
	assertEqual(t, "tested commit of the healthy project", healthyProject.lastTestedCommit(), healthyCommit)
}

func TestProjectWhoseStateCannotBeUsedMakesThePollFailButTheNextProjectStillRuns(t *testing.T) {
	harness := newPollHarness(t)
	brokenStateProject := harness.enrolProject("broken-state-project", "master")
	brokenStateProject.commitPassingStepOnTrunk()
	healthyProject := harness.enrolProject("healthy-project", "master")
	healthyCommit := healthyProject.commitPassingStepOnTrunk()
	if err := os.MkdirAll(brokenStateProject.stateDirectory(), 0o755); err != nil {
		t.Fatal(err)
	}
	fileWhereAFolderIsNeeded := filepath.Join(brokenStateProject.stateDirectory(), "unreported-results")
	if err := os.WriteFile(fileWhereAFolderIsNeeded, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := harness.pollReturningError()

	if err == nil {
		t.Error("the poll should return an error so that cron can tell someone")
	}
	assertEqual(t, "tested commit of the broken project", brokenStateProject.lastTestedCommit(), "")
	assertEqual(t, "tested commit of the healthy project", healthyProject.lastTestedCommit(), healthyCommit)
}

func TestTwoProjectsKeepSeparateClonesStateLogsAndFailureIssues(t *testing.T) {
	harness := newPollHarness(t)
	firstProject := harness.enrolProject("project-one", "master")
	secondProject := harness.enrolProject("project-two", "master")
	firstCommit := firstProject.commitFailingStepOnTrunk()
	secondCommit := secondProject.commitFailingStepOnTrunk()

	harness.poll()

	assertPathExists(t, filepath.Join(firstProject.cloneDirectory(), ".git"))
	assertPathExists(t, filepath.Join(secondProject.cloneDirectory(), ".git"))
	assertEqual(t, "first tested commit", firstProject.lastTestedCommit(), firstCommit)
	assertEqual(t, "second tested commit", secondProject.lastTestedCommit(), secondCommit)
	assertEqual(t, "first run logs", len(firstProject.runIdentifiers()), 1)
	assertEqual(t, "second run logs", len(secondProject.runIdentifiers()), 1)
	assertContains(t, "first issue", firstProject.onlyFailureIssue().Body, firstCommit)
	assertDoesNotContain(t, "first issue", firstProject.onlyFailureIssue().Body, secondCommit)
	assertContains(t, "second issue", secondProject.onlyFailureIssue().Body, secondCommit)
	assertDoesNotContain(t, "second issue", secondProject.onlyFailureIssue().Body, firstCommit)
}
