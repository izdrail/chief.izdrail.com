package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultBaseURL = "https://api.github.com"

// Client is a client for the GitHub API.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient creates a new GitHub API client.
func NewClient(token string) *Client {
	return &Client{
		BaseURL: defaultBaseURL,
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) doRequest(method, endpoint string, body interface{}, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.BaseURL+endpoint, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		var ghErr GitHubErrorResponse
		if json.Unmarshal(respBody, &ghErr) == nil && ghErr.Message != "" {
			return fmt.Errorf("github api error %d: %s", resp.StatusCode, ghErr.Message)
		}
		return fmt.Errorf("github api error %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}

	return nil
}

// CreatePullRequest creates a pull request.
// POST /repos/{owner}/{repo}/pulls
func (c *Client) CreatePullRequest(owner, repo string, req PullRequestRequest) (*PullRequestResponse, error) {
	endpoint := fmt.Sprintf("/repos/%s/%s/pulls", owner, repo)
	var resp PullRequestResponse
	if err := c.doRequest(http.MethodPost, endpoint, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateIssue creates an issue.
// POST /repos/{owner}/{repo}/issues
func (c *Client) CreateIssue(owner, repo string, req IssueRequest) (*IssueResponse, error) {
	endpoint := fmt.Sprintf("/repos/%s/%s/issues", owner, repo)
	var resp IssueResponse
	if err := c.doRequest(http.MethodPost, endpoint, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListIssues lists issues for a repository.
// GET /repos/{owner}/{repo}/issues
func (c *Client) ListIssues(owner, repo, state string) ([]IssueResponse, error) {
	endpoint := fmt.Sprintf("/repos/%s/%s/issues?per_page=100", owner, repo)
	if state != "" {
		endpoint += "&state=" + state
	}
	var resp []IssueResponse
	if err := c.doRequest(http.MethodGet, endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetIssue gets a single issue.
// GET /repos/{owner}/{repo}/issues/{issue_number}
func (c *Client) GetIssue(owner, repo string, number int) (*IssueResponse, error) {
	endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
	var resp IssueResponse
	if err := c.doRequest(http.MethodGet, endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListRepos lists repositories for the authenticated user.
// GET /user/repos
func (c *Client) ListRepos() ([]RepositoryResponse, error) {
	var resp []RepositoryResponse
	if err := c.doRequest(http.MethodGet, "/user/repos?per_page=100&sort=updated", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListPullRequests lists pull requests for a repository.
// GET /repos/{owner}/{repo}/pulls
func (c *Client) ListPullRequests(owner, repo, state string) ([]PullRequestResponse, error) {
	endpoint := fmt.Sprintf("/repos/%s/%s/pulls?per_page=100", owner, repo)
	if state != "" {
		endpoint += "&state=" + state
	}
	var resp []PullRequestResponse
	if err := c.doRequest(http.MethodGet, endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetPullRequest gets a single pull request.
// GET /repos/{owner}/{repo}/pulls/{pull_number}
func (c *Client) GetPullRequest(owner, repo string, number int) (*PullRequestResponse, error) {
	endpoint := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	var resp PullRequestResponse
	if err := c.doRequest(http.MethodGet, endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- Request / Response types matching GitHub's REST API schema ---

// GitHubErrorResponse represents an error returned by the GitHub API.
type GitHubErrorResponse struct {
	Message          string `json:"message"`
	DocumentationURL string `json:"documentation_url,omitempty"`
}

// PullRequestRequest is the payload for creating a pull request.
type PullRequestRequest struct {
	Title               string `json:"title"`
	Head                string `json:"head"`                          // source branch (e.g. "feature/my-branch")
	Base                string `json:"base"`                          // target branch (e.g. "main")
	Body                string `json:"body,omitempty"`
	Draft               bool   `json:"draft,omitempty"`
	MaintainerCanModify bool   `json:"maintainer_can_modify,omitempty"`
}

// PullRequestResponse matches GitHub's pull request object.
type PullRequestResponse struct {
	ID        int        `json:"id"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	Draft     bool       `json:"draft"`
	HTMLURL   string     `json:"html_url"`
	Head      struct {
		Ref string `json:"ref"` // branch name
		SHA string `json:"sha"`
	} `json:"head"`
	Base      struct {
		Ref string `json:"ref"` // target branch name
		SHA string `json:"sha"`
	} `json:"base"`
	User      GitHubUser `json:"user"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
	MergedAt  string     `json:"merged_at,omitempty"`
}

// IssueRequest is the payload for creating an issue.
type IssueRequest struct {
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Milestone *int     `json:"milestone,omitempty"`
}

// IssueResponse matches GitHub's issue object.
type IssueResponse struct {
	ID        int        `json:"id"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	HTMLURL   string     `json:"html_url"`
	User      GitHubUser `json:"user"`
	Labels    []struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"labels"`
	Assignees []GitHubUser `json:"assignees"`
	CreatedAt string       `json:"created_at"`
	UpdatedAt string       `json:"updated_at"`
	ClosedAt  string       `json:"closed_at,omitempty"`
}

// RepositoryResponse matches GitHub's repository object.
type RepositoryResponse struct {
	ID            int        `json:"id"`
	Name          string     `json:"name"`
	FullName      string     `json:"full_name"`
	Owner         GitHubUser `json:"owner"`
	Private       bool       `json:"private"`
	HTMLURL       string     `json:"html_url"`
	CloneURL      string     `json:"clone_url"`
	SSHURL        string     `json:"ssh_url"`
	Description   string     `json:"description,omitempty"`
	Fork          bool       `json:"fork"`
	CreatedAt     string     `json:"created_at"`
	UpdatedAt     string     `json:"updated_at"`
	Language      string     `json:"language,omitempty"`
	DefaultBranch string     `json:"default_branch"`
}

// GitHubUser is a minimal representation of a GitHub user object.
type GitHubUser struct {
	Login   string `json:"login"`
	ID      int    `json:"id"`
	HTMLURL string `json:"html_url"`
}
