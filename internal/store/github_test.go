package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

const fakeTokenForGitHubTests = "fake-token-for-tests-only"

var gitHubTestProject = domain.Project{
	Name:             "example-project",
	GitHubOwner:      "example-owner",
	GitHubRepository: "example-repository",
	TrunkBranch:      "master",
}

type capturedGitHubRequest struct {
	Method  string
	Path    string
	Query   map[string][]string
	Headers http.Header
	Body    string
}

type gitHubRequestCapture struct {
	mutex    sync.Mutex
	requests []capturedGitHubRequest
}

func (capture *gitHubRequestCapture) record(request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	capture.requests = append(capture.requests, capturedGitHubRequest{
		Method:  request.Method,
		Path:    request.URL.Path,
		Query:   request.URL.Query(),
		Headers: request.Header.Clone(),
		Body:    string(body),
	})
}

func (capture *gitHubRequestCapture) all() []capturedGitHubRequest {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	return append([]capturedGitHubRequest{}, capture.requests...)
}

func newGitHubStoreAgainstStub(t *testing.T, respond http.HandlerFunc) (GitHubStore, *gitHubRequestCapture) {
	t.Helper()
	capture := &gitHubRequestCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capture.record(request)
		respond(writer, request)
	}))
	t.Cleanup(server.Close)
	tokenFile := filepath.Join(t.TempDir(), "github-token")
	if err := os.WriteFile(tokenFile, []byte(fakeTokenForGitHubTests+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return GitHubStore{
		ApiBaseUrl:          server.URL,
		TokenFile:           tokenFile,
		CommitStatusContext: "ci/citask",
		RequestTimeLimit:    10 * time.Second,
	}, capture
}

func respondCreated(writer http.ResponseWriter, request *http.Request) {
	writer.WriteHeader(http.StatusCreated)
}

func decodeJsonBody(t *testing.T, body string) map[string]any {
	t.Helper()
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return decoded
}

func TestPublishingACommitStatusPostsItWithTheConfiguredContext(t *testing.T) {
	gitHubStore, capture := newGitHubStoreAgainstStub(t, respondCreated)

	err := gitHubStore.PublishCommitStatus(context.Background(), gitHubTestProject, "094b1e7c", domain.CommitStatus{
		State:       domain.CommitStatusFailure,
		Description: "Failed: go-tests",
	})

	if err != nil {
		t.Fatal(err)
	}
	requests := capture.all()
	if len(requests) != 1 {
		t.Fatalf("expected one request, got %+v", requests)
	}
	request := requests[0]
	if request.Method != http.MethodPost || request.Path != "/repos/example-owner/example-repository/statuses/094b1e7c" {
		t.Errorf("request: %s %s", request.Method, request.Path)
	}
	body := decodeJsonBody(t, request.Body)
	if body["state"] != "failure" || body["context"] != "ci/citask" || body["description"] != "Failed: go-tests" {
		t.Errorf("body: %v", body)
	}
	if got := request.Headers.Get("Authorization"); got != "Bearer "+fakeTokenForGitHubTests {
		t.Errorf("authorization header: %q", got)
	}
	if got := request.Headers.Get("Accept"); got != "application/vnd.github+json" {
		t.Errorf("accept header: %q", got)
	}
	if got := request.Headers.Get("X-GitHub-Api-Version"); got == "" {
		t.Error("the API version should be pinned")
	}
}

func TestTokenIsReadAtTheMomentOfUseSoAReplacedTokenTakesEffect(t *testing.T) {
	gitHubStore, capture := newGitHubStoreAgainstStub(t, respondCreated)
	publish := func() error {
		return gitHubStore.PublishCommitStatus(context.Background(), gitHubTestProject, "094b1e7c", domain.CommitStatus{State: domain.CommitStatusPending})
	}
	if err := publish(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitHubStore.TokenFile, []byte("replacement-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := publish(); err != nil {
		t.Fatal(err)
	}

	if got := capture.all()[1].Headers.Get("Authorization"); got != "Bearer replacement-token" {
		t.Errorf("authorization header: %q", got)
	}
}

func TestMissingOrEmptyTokenFailsWithoutSendingARequest(t *testing.T) {
	gitHubStore, capture := newGitHubStoreAgainstStub(t, respondCreated)
	publish := func() error {
		return gitHubStore.PublishCommitStatus(context.Background(), gitHubTestProject, "094b1e7c", domain.CommitStatus{State: domain.CommitStatusPending})
	}

	if err := os.WriteFile(gitHubStore.TokenFile, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publish(); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("expected a token error, got %v", err)
	}

	if err := os.Remove(gitHubStore.TokenFile); err != nil {
		t.Fatal(err)
	}
	if err := publish(); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("expected a token error, got %v", err)
	}

	if requests := capture.all(); len(requests) != 0 {
		t.Errorf("no request should be sent without a token: %+v", requests)
	}
}

func TestRejectedRequestReturnsAnErrorNamingTheStatusButNotTheToken(t *testing.T) {
	gitHubStore, _ := newGitHubStoreAgainstStub(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "Bad credentials", http.StatusUnauthorized)
	})

	err := gitHubStore.PublishCommitStatus(context.Background(), gitHubTestProject, "094b1e7c", domain.CommitStatus{State: domain.CommitStatusPending})

	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected the status to be named, got %v", err)
	}
	if strings.Contains(err.Error(), fakeTokenForGitHubTests) {
		t.Errorf("the token must not appear in an error: %v", err)
	}
}

func TestGitHubThatNeverRespondsGivesUpAtTheRequestTimeLimit(t *testing.T) {
	gitHubStore, _ := newGitHubStoreAgainstStub(t, func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	})
	gitHubStore.RequestTimeLimit = 200 * time.Millisecond

	startedAt := time.Now()
	err := gitHubStore.PublishCommitStatus(context.Background(), gitHubTestProject, "094b1e7c", domain.CommitStatus{State: domain.CommitStatusPending})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the time limit to be reached, got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 5*time.Second {
		t.Errorf("giving up took %s", elapsed)
	}
}

type stubGitHubListItem struct {
	Number      int            `json:"number"`
	Title       string         `json:"title"`
	PullRequest map[string]any `json:"pull_request,omitempty"`
}

func respondWithPagedList(items []stubGitHubListItem) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		perPage, _ := strconv.Atoi(request.URL.Query().Get("per_page"))
		page, _ := strconv.Atoi(request.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		firstIndex := min((page-1)*perPage, len(items))
		lastIndex := min(firstIndex+perPage, len(items))
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(items[firstIndex:lastIndex])
	}
}

func TestFindingTheFailureIssueAsksForOpenIssuesAndMatchesTheExactTitle(t *testing.T) {
	gitHubStore, capture := newGitHubStoreAgainstStub(t, respondWithPagedList([]stubGitHubListItem{
		{Number: 1, Title: "CI failing on master (citask) again"},
		{Number: 2, Title: "ci failing on master (citask)"},
		{Number: 3, Title: "CI failing on other-branch (citask)"},
		{Number: 4, Title: "CI failing on master (citask)"},
	}))

	issueNumber, found, err := gitHubStore.FindOpenFailureIssue(context.Background(), gitHubTestProject)

	if err != nil {
		t.Fatal(err)
	}
	if !found || issueNumber != 4 {
		t.Errorf("got issue %d, found %v", issueNumber, found)
	}
	request := capture.all()[0]
	if request.Method != http.MethodGet || request.Path != "/repos/example-owner/example-repository/issues" {
		t.Errorf("request: %s %s", request.Method, request.Path)
	}
	if request.Query["state"][0] != "open" || request.Query["per_page"][0] != "100" {
		t.Errorf("query: %v", request.Query)
	}
}

func TestFindingTheFailureIssueFollowsPages(t *testing.T) {
	items := []stubGitHubListItem{}
	for itemNumber := 1; itemNumber <= 100; itemNumber++ {
		items = append(items, stubGitHubListItem{Number: itemNumber, Title: fmt.Sprintf("unrelated issue %d", itemNumber)})
	}
	items = append(items, stubGitHubListItem{Number: 101, Title: "CI failing on master (citask)"})
	gitHubStore, capture := newGitHubStoreAgainstStub(t, respondWithPagedList(items))

	issueNumber, found, err := gitHubStore.FindOpenFailureIssue(context.Background(), gitHubTestProject)

	if err != nil {
		t.Fatal(err)
	}
	if !found || issueNumber != 101 {
		t.Errorf("got issue %d, found %v", issueNumber, found)
	}
	if requests := capture.all(); len(requests) != 2 {
		t.Errorf("expected two pages to be read, got %d", len(requests))
	}
}

func TestFindingTheFailureIssueIgnoresAPullRequestWithTheSameTitle(t *testing.T) {
	gitHubStore, _ := newGitHubStoreAgainstStub(t, respondWithPagedList([]stubGitHubListItem{
		{Number: 1, Title: "CI failing on master (citask)", PullRequest: map[string]any{"url": "https://api.github.example/pulls/1"}},
	}))

	_, found, err := gitHubStore.FindOpenFailureIssue(context.Background(), gitHubTestProject)

	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("a pull request is not the failure issue")
	}
}

func TestFindingTheFailureIssueReportsWhenThereIsNone(t *testing.T) {
	gitHubStore, _ := newGitHubStoreAgainstStub(t, respondWithPagedList(nil))

	_, found, err := gitHubStore.FindOpenFailureIssue(context.Background(), gitHubTestProject)

	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("there is no failure issue")
	}
}

func TestFindingTheFailureIssueReturnsARejection(t *testing.T) {
	gitHubStore, _ := newGitHubStoreAgainstStub(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "stub outage", http.StatusBadGateway)
	})

	if _, _, err := gitHubStore.FindOpenFailureIssue(context.Background(), gitHubTestProject); err == nil {
		t.Fatal("expected an error")
	}
}

func TestOpeningTheFailureIssueUsesTheExactTitle(t *testing.T) {
	gitHubStore, capture := newGitHubStoreAgainstStub(t, respondCreated)

	if err := gitHubStore.OpenFailureIssue(context.Background(), gitHubTestProject, "issue body"); err != nil {
		t.Fatal(err)
	}

	request := capture.all()[0]
	if request.Method != http.MethodPost || request.Path != "/repos/example-owner/example-repository/issues" {
		t.Errorf("request: %s %s", request.Method, request.Path)
	}
	body := decodeJsonBody(t, request.Body)
	if body["title"] != "CI failing on master (citask)" || body["body"] != "issue body" {
		t.Errorf("body: %v", body)
	}
}

func TestCommentingOnTheFailureIssuePostsTheComment(t *testing.T) {
	gitHubStore, capture := newGitHubStoreAgainstStub(t, respondCreated)

	if err := gitHubStore.CommentOnFailureIssue(context.Background(), gitHubTestProject, 7, "comment body"); err != nil {
		t.Fatal(err)
	}

	request := capture.all()[0]
	if request.Method != http.MethodPost || request.Path != "/repos/example-owner/example-repository/issues/7/comments" {
		t.Errorf("request: %s %s", request.Method, request.Path)
	}
	if body := decodeJsonBody(t, request.Body); body["body"] != "comment body" {
		t.Errorf("body: %v", body)
	}
}

func TestClosingTheFailureIssueCommentsThenClosesAsCompleted(t *testing.T) {
	gitHubStore, capture := newGitHubStoreAgainstStub(t, respondCreated)

	if err := gitHubStore.CloseFailureIssue(context.Background(), gitHubTestProject, 7, "closing comment"); err != nil {
		t.Fatal(err)
	}

	requests := capture.all()
	if len(requests) != 2 {
		t.Fatalf("expected two requests, got %+v", requests)
	}
	if requests[0].Path != "/repos/example-owner/example-repository/issues/7/comments" {
		t.Errorf("first request: %s %s", requests[0].Method, requests[0].Path)
	}
	if requests[1].Method != http.MethodPatch || requests[1].Path != "/repos/example-owner/example-repository/issues/7" {
		t.Errorf("second request: %s %s", requests[1].Method, requests[1].Path)
	}
	body := decodeJsonBody(t, requests[1].Body)
	if body["state"] != "closed" || body["state_reason"] != "completed" {
		t.Errorf("body: %v", body)
	}
}

func TestIssueIsNotClosedWhenTheClosingCommentFails(t *testing.T) {
	gitHubStore, capture := newGitHubStoreAgainstStub(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "stub outage", http.StatusBadGateway)
	})

	if err := gitHubStore.CloseFailureIssue(context.Background(), gitHubTestProject, 7, "closing comment"); err == nil {
		t.Fatal("expected an error")
	}

	if requests := capture.all(); len(requests) != 1 {
		t.Errorf("the issue must stay open if its comment fails: %+v", requests)
	}
}
