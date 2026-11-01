package application_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type gitHubStubBehaviour int

const (
	respondNormally gitHubStubBehaviour = iota
	rejectEveryRequestAsUnauthorized
	failEveryRequestWithServerError
	neverRespond
)

type recordedRequest struct {
	Method string
	Url    string
	Header http.Header
	Body   string
}

type recordedCommitStatus struct {
	CommitSha   string
	State       string
	Context     string
	Description string
}

type stubPullRequestMarker struct {
	Url string `json:"url"`
}

type stubIssue struct {
	Number      int                    `json:"number"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body"`
	State       string                 `json:"state"`
	StateReason string                 `json:"state_reason,omitempty"`
	PullRequest *stubPullRequestMarker `json:"pull_request,omitempty"`
	Comments    []string               `json:"-"`
}

type stubRepository struct {
	commitStatuses []recordedCommitStatus
	issues         []*stubIssue
}

type gitHubStub struct {
	server       *httptest.Server
	routes       *http.ServeMux
	mutex        sync.Mutex
	behaviour    gitHubStubBehaviour
	requests     []recordedRequest
	repositories map[string]*stubRepository
}

func newGitHubStub(t *testing.T) *gitHubStub {
	t.Helper()
	stub := &gitHubStub{repositories: map[string]*stubRepository{}, routes: http.NewServeMux()}
	stub.routes.HandleFunc("POST /repos/{owner}/{repo}/statuses/{sha}", stub.handleCreateCommitStatus)
	stub.routes.HandleFunc("GET /repos/{owner}/{repo}/issues", stub.handleListIssues)
	stub.routes.HandleFunc("POST /repos/{owner}/{repo}/issues", stub.handleCreateIssue)
	stub.routes.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/comments", stub.handleCreateIssueComment)
	stub.routes.HandleFunc("PATCH /repos/{owner}/{repo}/issues/{number}", stub.handleUpdateIssue)
	stub.server = httptest.NewServer(stub)
	t.Cleanup(stub.server.Close)
	return stub
}

func (stub *gitHubStub) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	request.Body = io.NopCloser(bytes.NewReader(body))

	stub.mutex.Lock()
	behaviour := stub.behaviour
	stub.requests = append(stub.requests, recordedRequest{
		Method: request.Method,
		Url:    request.URL.String(),
		Header: request.Header.Clone(),
		Body:   string(body),
	})
	stub.mutex.Unlock()

	switch behaviour {
	case neverRespond:
		// Returning when the client gives up keeps server.Close from waiting forever on this request.
		<-request.Context().Done()
		return
	case rejectEveryRequestAsUnauthorized:
		http.Error(writer, "Bad credentials", http.StatusUnauthorized)
		return
	case failEveryRequestWithServerError:
		http.Error(writer, "stub outage", http.StatusBadGateway)
		return
	}

	if request.Header.Get("Authorization") != "Bearer "+fakeGitHubToken {
		http.Error(writer, "Bad credentials", http.StatusUnauthorized)
		return
	}
	stub.routes.ServeHTTP(writer, request)
}

func (stub *gitHubStub) repositoryOfRequest(request *http.Request) *stubRepository {
	return stub.repositoryNamed(request.PathValue("owner"), request.PathValue("repo"))
}

func (stub *gitHubStub) repositoryNamed(owner, repository string) *stubRepository {
	key := owner + "/" + repository
	if _, exists := stub.repositories[key]; !exists {
		stub.repositories[key] = &stubRepository{}
	}
	return stub.repositories[key]
}

func (stub *gitHubStub) handleCreateCommitStatus(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		State       string `json:"state"`
		Context     string `json:"context"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(writer, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if len(body.Description) > 140 {
		http.Error(writer, "description is too long (maximum is 140 characters)", http.StatusUnprocessableEntity)
		return
	}
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	repository := stub.repositoryOfRequest(request)
	repository.commitStatuses = append(repository.commitStatuses, recordedCommitStatus{
		CommitSha:   request.PathValue("sha"),
		State:       body.State,
		Context:     body.Context,
		Description: body.Description,
	})
	writer.WriteHeader(http.StatusCreated)
}

func (stub *gitHubStub) handleListIssues(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	if query.Get("state") != "open" {
		http.Error(writer, "the stub only lists open issues", http.StatusBadRequest)
		return
	}
	perPage, err := strconv.Atoi(query.Get("per_page"))
	if err != nil || perPage < 1 {
		http.Error(writer, "per_page is required", http.StatusBadRequest)
		return
	}
	page := 1
	if requestedPage := query.Get("page"); requestedPage != "" {
		if page, err = strconv.Atoi(requestedPage); err != nil {
			http.Error(writer, "page must be a number", http.StatusBadRequest)
			return
		}
	}

	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	openIssues := []*stubIssue{}
	for _, issue := range stub.repositoryOfRequest(request).issues {
		if issue.State == "open" {
			openIssues = append(openIssues, issue)
		}
	}
	firstIndex := min((page-1)*perPage, len(openIssues))
	lastIndex := min(firstIndex+perPage, len(openIssues))
	writer.Header().Set("Content-Type", "application/json")
	json.NewEncoder(writer).Encode(openIssues[firstIndex:lastIndex])
}

func (stub *gitHubStub) handleCreateIssue(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(writer, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	repository := stub.repositoryOfRequest(request)
	issue := &stubIssue{Number: len(repository.issues) + 1, Title: body.Title, Body: body.Body, State: "open"}
	repository.issues = append(repository.issues, issue)
	writer.WriteHeader(http.StatusCreated)
	json.NewEncoder(writer).Encode(issue)
}

func (stub *gitHubStub) issueOfRequest(request *http.Request) *stubIssue {
	number, err := strconv.Atoi(request.PathValue("number"))
	if err != nil {
		return nil
	}
	for _, issue := range stub.repositoryOfRequest(request).issues {
		if issue.Number == number {
			return issue
		}
	}
	return nil
}

func (stub *gitHubStub) handleCreateIssueComment(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(writer, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	issue := stub.issueOfRequest(request)
	if issue == nil {
		http.NotFound(writer, request)
		return
	}
	issue.Comments = append(issue.Comments, body.Body)
	writer.WriteHeader(http.StatusCreated)
}

func (stub *gitHubStub) handleUpdateIssue(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		State       string `json:"state"`
		StateReason string `json:"state_reason"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(writer, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	issue := stub.issueOfRequest(request)
	if issue == nil {
		http.NotFound(writer, request)
		return
	}
	issue.State = body.State
	issue.StateReason = body.StateReason
	writer.WriteHeader(http.StatusOK)
}

func (stub *gitHubStub) setBehaviour(behaviour gitHubStubBehaviour) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.behaviour = behaviour
}

func (stub *gitHubStub) requestCount() int {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return len(stub.requests)
}

func (stub *gitHubStub) textOfEveryRequest() string {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	var everything strings.Builder
	for _, request := range stub.requests {
		fmt.Fprintln(&everything, request.Method, request.Url, request.Body)
		for headerName, headerValues := range request.Header {
			if headerName != "Authorization" {
				fmt.Fprintln(&everything, headerName, headerValues)
			}
		}
	}
	return everything.String()
}

func (stub *gitHubStub) commitStatuses(owner, repository string) []recordedCommitStatus {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return append([]recordedCommitStatus{}, stub.repositoryNamed(owner, repository).commitStatuses...)
}

func (stub *gitHubStub) issuesExcludingPullRequests(owner, repository string) []stubIssue {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	issues := []stubIssue{}
	for _, issue := range stub.repositoryNamed(owner, repository).issues {
		if issue.PullRequest == nil {
			issues = append(issues, *issue)
		}
	}
	return issues
}

func (stub *gitHubStub) addOpenPullRequest(owner, repository, title string) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stubbedRepository := stub.repositoryNamed(owner, repository)
	stubbedRepository.issues = append(stubbedRepository.issues, &stubIssue{
		Number:      len(stubbedRepository.issues) + 1,
		Title:       title,
		State:       "open",
		PullRequest: &stubPullRequestMarker{Url: "https://api.example.com/pulls/1"},
	})
}
