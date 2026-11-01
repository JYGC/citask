package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

const (
	issuesPerPage                = 100
	gitHubApiVersion             = "2022-11-28"
	longestResponseKeptInAnError = 200
)

type GitHubStore struct {
	ApiBaseUrl          string
	TokenFile           string
	CommitStatusContext string
	RequestTimeLimit    time.Duration
}

func failureIssueTitle(project domain.Project) string {
	return "CI failing on " + project.TrunkBranch + " (citask)"
}

func repositoryPath(project domain.Project) string {
	return "/repos/" + project.GitHubOwner + "/" + project.GitHubRepository
}

func (gitHubStore GitHubStore) PublishCommitStatus(ctx context.Context, project domain.Project, commitSha string, status domain.CommitStatus) error {
	_, err := gitHubStore.sendRequest(ctx, http.MethodPost, repositoryPath(project)+"/statuses/"+commitSha, map[string]string{
		"state":       string(status.State),
		"context":     gitHubStore.CommitStatusContext,
		"description": status.Description,
	})
	return err
}

// FindOpenFailureIssue finds the issue by its exact title, so it survives a lost state file. It asks for
// numbered pages rather than following the Link header, so the token only ever goes to ApiBaseUrl.
func (gitHubStore GitHubStore) FindOpenFailureIssue(ctx context.Context, project domain.Project) (int, bool, error) {
	expectedTitle := failureIssueTitle(project)
	for page := 1; ; page++ {
		query := url.Values{
			"state":    {"open"},
			"per_page": {strconv.Itoa(issuesPerPage)},
			"page":     {strconv.Itoa(page)},
		}
		responseBody, err := gitHubStore.sendRequest(ctx, http.MethodGet, repositoryPath(project)+"/issues?"+query.Encode(), nil)
		if err != nil {
			return 0, false, err
		}
		var issues []struct {
			Number      int             `json:"number"`
			Title       string          `json:"title"`
			PullRequest json.RawMessage `json:"pull_request"`
		}
		if err := json.Unmarshal(responseBody, &issues); err != nil {
			return 0, false, fmt.Errorf("decode issues of %s: %w", project.Name, err)
		}
		for _, issue := range issues {
			if issue.Title == expectedTitle && issue.PullRequest == nil {
				return issue.Number, true, nil
			}
		}
		if len(issues) < issuesPerPage {
			return 0, false, nil
		}
	}
}

func (gitHubStore GitHubStore) OpenFailureIssue(ctx context.Context, project domain.Project, body string) error {
	_, err := gitHubStore.sendRequest(ctx, http.MethodPost, repositoryPath(project)+"/issues", map[string]string{
		"title": failureIssueTitle(project),
		"body":  body,
	})
	return err
}

func (gitHubStore GitHubStore) CommentOnFailureIssue(ctx context.Context, project domain.Project, issueNumber int, body string) error {
	path := fmt.Sprintf("%s/issues/%d/comments", repositoryPath(project), issueNumber)
	_, err := gitHubStore.sendRequest(ctx, http.MethodPost, path, map[string]string{"body": body})
	return err
}

func (gitHubStore GitHubStore) CloseFailureIssue(ctx context.Context, project domain.Project, issueNumber int, closingComment string) error {
	if err := gitHubStore.CommentOnFailureIssue(ctx, project, issueNumber, closingComment); err != nil {
		return err
	}
	path := fmt.Sprintf("%s/issues/%d", repositoryPath(project), issueNumber)
	_, err := gitHubStore.sendRequest(ctx, http.MethodPatch, path, map[string]string{
		"state":        "closed",
		"state_reason": "completed",
	})
	return err
}

func (gitHubStore GitHubStore) sendRequest(ctx context.Context, method, path string, jsonBody map[string]string) ([]byte, error) {
	token, err := gitHubStore.readToken()
	if err != nil {
		return nil, err
	}
	requestBody, err := encodeRequestBody(jsonBody)
	if err != nil {
		return nil, err
	}

	requestContext, cancel := context.WithTimeout(ctx, gitHubStore.RequestTimeLimit)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, method, gitHubStore.ApiBaseUrl+path, requestBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", gitHubApiVersion)
	if jsonBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read response to %s %s: %w", method, path, err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("GitHub answered %d to %s %s: %s", response.StatusCode, method, path, shortenedForAnError(responseBody))
	}
	return responseBody, nil
}

func (gitHubStore GitHubStore) readToken() (string, error) {
	content, err := os.ReadFile(gitHubStore.TokenFile)
	if err != nil {
		return "", fmt.Errorf("read GitHub token: %w", err)
	}
	token := strings.TrimSpace(string(content))
	if token == "" {
		return "", errors.New("GitHub token file is empty")
	}
	return token, nil
}

func encodeRequestBody(jsonBody map[string]string) (io.Reader, error) {
	if jsonBody == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(jsonBody)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return bytes.NewReader(encoded), nil
}

func shortenedForAnError(responseBody []byte) string {
	return strings.TrimSpace(string(responseBody[:min(longestResponseKeptInAnError, len(responseBody))]))
}
