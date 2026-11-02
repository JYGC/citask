package application_test

import (
	"os"
	"strings"
	"testing"
	"time"
)

const heartbeatPathOfATestCheck = "/ping/example-check-id"

func newHarnessWithHeartbeat(t *testing.T) (*pollHarness, *heartbeatStub) {
	t.Helper()
	harness := newPollHarness(t)
	heartbeat := newHeartbeatStub(t, harness.github)
	harness.heartbeatUrl = heartbeat.server.URL + heartbeatPathOfATestCheck
	return harness, heartbeat
}

func TestPassingRunSendsOneHeartbeatAfterTheResultIsPosted(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	project := harness.enrolProject("project-one", "master")
	commitSha := project.commitPassingStepOnTrunk()

	harness.poll()

	assertEqual(t, "heartbeats", heartbeat.count(), 1)
	assertStringSlicesEqual(t, "statuses", project.commitStatusStatesFor(commitSha), []string{"pending", "success"})
	assertEqual(t, "GitHub requests made before the heartbeat", heartbeat.gitHubRequestsAtLastHeartbeat(), harness.github.requestCount())
}

func TestFailingBuildStillSendsAHeartbeatBecauseCiDidItsJob(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	project := harness.enrolProject("project-one", "master")
	project.commitFailingStepOnTrunk()

	harness.poll()

	assertEqual(t, "failure issues", len(project.failureIssues()), 1)
	assertEqual(t, "heartbeats", heartbeat.count(), 1)
}

func TestPollWithNothingNewSendsAHeartbeat(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	project := harness.enrolProject("project-one", "master")
	project.commitPassingStepOnTrunk()
	harness.poll()
	requestCountAfterTheRun := harness.github.requestCount()

	harness.poll()

	assertEqual(t, "heartbeats", heartbeat.count(), 2)
	assertEqual(t, "GitHub requests on the quiet poll", harness.github.requestCount(), requestCountAfterTheRun)
}

func TestUnreadableTrunkSendsNoHeartbeatUntilItRecovers(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	project := harness.enrolProject("project-one", "master")
	project.commitPassingStepOnTrunk()
	harness.poll()
	assertEqual(t, "heartbeats after the healthy poll", heartbeat.count(), 1)

	unreachableDirectory := project.bareRepositoryDirectory + ".unreachable"
	if err := os.Rename(project.bareRepositoryDirectory, unreachableDirectory); err != nil {
		t.Fatal(err)
	}
	harness.poll()
	assertEqual(t, "heartbeats while the trunk cannot be read", heartbeat.count(), 1)

	if err := os.Rename(unreachableDirectory, project.bareRepositoryDirectory); err != nil {
		t.Fatal(err)
	}
	harness.poll()
	assertEqual(t, "heartbeats after recovery", heartbeat.count(), 2)
}

func TestFailedCheckoutSendsNoHeartbeat(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	project := harness.enrolProject("project-one", "master")
	project.commitPassingStepOnTrunk()
	writeFileWhereTheCloneMustGo(t, project)

	harness.poll()

	assertEqual(t, "heartbeats", heartbeat.count(), 0)
}

func writeFileWhereTheCloneMustGo(t *testing.T, project *enrolledProject) {
	t.Helper()
	if err := os.MkdirAll(project.harness.ciHomeDirectory+"/projects", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project.cloneDirectory(), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedTokenSendsNoHeartbeatUntilTheResultIsReplayed(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	project := harness.enrolProject("project-one", "master")
	harness.github.setBehaviour(rejectEveryRequestAsUnauthorized)
	project.commitPassingStepOnTrunk()

	harness.poll()
	harness.poll()

	assertEqual(t, "results waiting", project.unreportedResultCount(), 1)
	assertEqual(t, "heartbeats while the result waits", heartbeat.count(), 0)

	harness.github.setBehaviour(respondNormally)
	harness.poll()

	assertEqual(t, "results waiting after replay", project.unreportedResultCount(), 0)
	assertEqual(t, "heartbeats after replay", heartbeat.count(), 1)
}

func TestResultWaitingInOneProjectWithholdsTheHeartbeatEvenWhenTheOtherProjectIsHealthy(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	firstProject := harness.enrolProject("project-one", "master")
	secondProject := harness.enrolProject("project-two", "master")
	firstProject.commitStepsOnTrunk(shellStep("first-project-step", "exit 0"))
	secondProject.commitStepsOnTrunk(shellStep("second-project-step", "exit 0"))
	harness.github.rejectRequestsForRepository(testGitHubOwner, firstProject.definition.GitHubRepository)

	harness.poll()

	assertEqual(t, "results waiting for the first project", firstProject.unreportedResultCount(), 1)
	assertEqual(t, "results waiting for the second project", secondProject.unreportedResultCount(), 0)
	assertEqual(t, "heartbeats", heartbeat.count(), 0)
}

func TestUnusableStateSendsNoHeartbeat(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	project := harness.enrolProject("project-one", "master")
	project.commitPassingStepOnTrunk()
	if err := os.MkdirAll(project.stateDirectory(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project.stateDirectory()+"/unreported-results", []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := harness.pollReturningError(); err == nil {
		t.Error("the poll should return an error")
	}

	assertEqual(t, "heartbeats", heartbeat.count(), 0)
}

func TestUnusableLogsSendNoHeartbeat(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	project := harness.enrolProject("project-one", "master")
	project.commitPassingStepOnTrunk()
	if err := os.MkdirAll(harness.ciHomeDirectory+"/logs", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project.logsDirectory(), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := harness.pollReturningError(); err == nil {
		t.Error("the poll should return an error")
	}

	assertEqual(t, "heartbeats", heartbeat.count(), 0)
}

func TestHeldLockSendsNoHeartbeat(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	project := harness.enrolProject("project-one", "master")
	project.commitPassingStepOnTrunk()
	releaseLock := harness.holdExclusivePollLock()

	harness.poll()

	assertEqual(t, "heartbeats while the lock is held", heartbeat.count(), 0)
	releaseLock()
	harness.poll()
	assertEqual(t, "heartbeats after the lock is freed", heartbeat.count(), 1)
}

func TestNoHeartbeatUrlMakesNoRequestAndPollsAsUsual(t *testing.T) {
	harness := newPollHarness(t)
	heartbeat := newHeartbeatStub(t, harness.github)
	project := harness.enrolProject("project-one", "master")
	commitSha := project.commitPassingStepOnTrunk()

	harness.poll()

	assertEqual(t, "heartbeats", heartbeat.count(), 0)
	assertEqual(t, "tested commit", project.lastTestedCommit(), commitSha)
}

func TestHeartbeatServiceFailureDoesNotAffectThePoll(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	heartbeat.setBehaviour(failEveryHeartbeat)
	project := harness.enrolProject("project-one", "master")
	commitSha := project.commitPassingStepOnTrunk()

	harness.poll()

	assertEqual(t, "heartbeat attempts", heartbeat.count(), 1)
	assertEqual(t, "tested commit", project.lastTestedCommit(), commitSha)
	assertStringSlicesEqual(t, "statuses", project.commitStatusStatesFor(commitSha), []string{"pending", "success"})
	assertEqual(t, "results waiting", project.unreportedResultCount(), 0)
}

func TestHeartbeatServiceThatNeverRespondsCannotHangThePoll(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	harness.heartbeatRequestTimeLimit = 200 * time.Millisecond
	heartbeat.setBehaviour(neverAnswerHeartbeats)
	project := harness.enrolProject("project-one", "master")
	commitSha := project.commitPassingStepOnTrunk()

	pollStartedAt := time.Now()
	harness.poll()

	if elapsed := time.Since(pollStartedAt); elapsed > generousBoundForAPoll {
		t.Errorf("a heartbeat service that never responds held the poll for %s", elapsed)
	}
	assertEqual(t, "tested commit", project.lastTestedCommit(), commitSha)
}

func TestHeartbeatIsABareRequestCarryingNothingButTheConfiguredUrl(t *testing.T) {
	harness, heartbeat := newHarnessWithHeartbeat(t)
	project := harness.enrolProject("project-with-a-private-name", "master")
	project.commitFailingStepOnTrunk()

	harness.poll()

	request := heartbeat.lastRequest()
	assertEqual(t, "method", request.Method, "GET")
	assertEqual(t, "url", request.Url, heartbeatPathOfATestCheck)
	assertEqual(t, "body", request.Body, "")
	assertEqual(t, "authorization header", request.Header.Get("Authorization"), "")
	everythingSent := request.Url + request.Body + strings.Join(request.Header.Values("User-Agent"), " ")
	for _, privateValue := range []string{"project-with-a-private-name", fakeGitHubToken, harness.ciHomeDirectory, "failing-step"} {
		assertDoesNotContain(t, "heartbeat request", everythingSent, privateValue)
	}
}
