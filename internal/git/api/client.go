package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client is a client for the Git API.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient creates a new Git API client.
func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = "https://git.izdrail.com"
	}
	return &Client{
		BaseURL: baseURL,
		Token:   token,
		HTTP:    &http.Client{},
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
		req.Header.Set("Authorization", "token "+c.Token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("api error %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}

	return nil
}

// CreatePullRequest creates a PR.
func (c *Client) CreatePullRequest(req PullRequestRequest) error {
	return c.doRequest(http.MethodPost, "/create-pull-request", req, nil)
}

// CreateIssue creates an issue.
func (c *Client) CreateIssue(req IssueRequest) error {
	return c.doRequest(http.MethodPost, "/create-issue", req, nil)
}

// SuggestFix requests a fix suggestion.
func (c *Client) SuggestFix(req FixSuggestionRequest) error {
	return c.doRequest(http.MethodPost, "/suggest-fix", req, nil)
}

// ListIssues lists issues.
func (c *Client) ListIssues(owner, repo, state string) ([]IssueResponse, error) {
	endpoint := fmt.Sprintf("/issues/list?owner=%s&repo=%s", owner, repo)
	if state != "" {
		endpoint += "&state=" + state
	}
	var resp []IssueResponse
	if err := c.doRequest(http.MethodGet, endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListRepos lists repositories.
func (c *Client) ListRepos() ([]RepositoryResponse, error) {
	var resp []RepositoryResponse
	if err := c.doRequest(http.MethodGet, "/repos/list", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListPullRequests lists pull requests.
func (c *Client) ListPullRequests(owner, repo, state string) ([]PullRequestResponse, error) {
	endpoint := fmt.Sprintf("/pulls/list?owner=%s&repo=%s", owner, repo)
	if state != "" {
		endpoint += "&state=" + state
	}
	var resp []PullRequestResponse
	if err := c.doRequest(http.MethodGet, endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Structures based on OpenAPI spec

type PullRequestRequest struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	Base        string `json:"base,omitempty"`
	BranchName  string `json:"branch_name"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	FilePath    string `json:"file_path"`
	FileContent string `json:"file_content"`
}

type PullRequestResponse struct {
	ID        int    `json:"id"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	HTMLURL   string `json:"html_url"`
	Branch    string `json:"branch"`
	Base      string `json:"base"`
}

type IssueRequest struct {
	Owner     string   `json:"owner"`
	Repo      string   `json:"repo"`
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
}

type FixSuggestionRequest struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	Model       string `json:"model,omitempty"`
}

type IssueResponse struct {
	// Add fields as needed based on actual API response, starting with map for flexibility
	ID     int    `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
}
type RepositoryResponse struct {
	ID    int    `json:"id"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
	Name     string `json:"name"`
	HTMLURL  string `json:"html_url"`
	CloneURL string `json:"clone_url"`
}
