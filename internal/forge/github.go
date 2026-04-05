// Package forge provides API clients for Git forge providers (GitHub, GitLab).
// These clients are used by the GitRepo controller to discover repositories
// via provider REST APIs.
//
// Design choices:
//   - Uses raw net/http to match codebase style (no third-party HTTP client libraries)
//   - All clients are stateless — credentials are passed per-call, not stored
//   - Pagination is handled automatically (follows Link headers / page params)
//   - Rate limiting is respected via Retry-After headers
package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/samyn92/agent-operator-core/internal/controller"
)

// Compile-time interface check.
var _ controller.GitHubAPIClient = (*GitHubClient)(nil)

const (
	githubAPIBaseURL = "https://api.github.com"
	// GitHub paginates at 100 per page max.
	githubPerPage = 100
	// Maximum pages to fetch to prevent runaway pagination.
	githubMaxPages = 50
)

// GitHubClient implements the GitHubAPIClient interface using the GitHub REST API v3.
type GitHubClient struct {
	httpClient *http.Client
	baseURL    string // overridable for testing / GitHub Enterprise
}

// GitHubOption configures a GitHubClient.
type GitHubOption func(*GitHubClient)

// WithGitHubBaseURL sets a custom API base URL (e.g., for GitHub Enterprise).
func WithGitHubBaseURL(url string) GitHubOption {
	return func(c *GitHubClient) {
		c.baseURL = strings.TrimRight(url, "/")
	}
}

// WithGitHubHTTPClient sets a custom HTTP client.
func WithGitHubHTTPClient(client *http.Client) GitHubOption {
	return func(c *GitHubClient) {
		c.httpClient = client
	}
}

// NewGitHubClient creates a new GitHub API client.
func NewGitHubClient(opts ...GitHubOption) *GitHubClient {
	c := &GitHubClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    githubAPIBaseURL,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// githubRepo is the JSON structure returned by GitHub's repos API.
type githubRepo struct {
	FullName      string   `json:"full_name"`
	CloneURL      string   `json:"clone_url"`
	SSHURL        string   `json:"ssh_url"`
	DefaultBranch string   `json:"default_branch"`
	Description   string   `json:"description"`
	Visibility    string   `json:"visibility"`
	Archived      bool     `json:"archived"`
	PushedAt      string   `json:"pushed_at"`
	Topics        []string `json:"topics"`
}

func (r *githubRepo) toRepoInfo() controller.RepoInfo {
	info := controller.RepoInfo{
		FullName:      r.FullName,
		CloneURL:      r.CloneURL,
		SSHURL:        r.SSHURL,
		DefaultBranch: r.DefaultBranch,
		Description:   r.Description,
		Visibility:    r.Visibility,
		Archived:      r.Archived,
		Topics:        r.Topics,
	}
	if t, err := time.Parse(time.RFC3339, r.PushedAt); err == nil {
		info.LastActivity = t
	}
	return info
}

// ListOrgRepos lists all repositories for an organization.
func (c *GitHubClient) ListOrgRepos(ctx context.Context, token, org string) ([]controller.RepoInfo, error) {
	url := fmt.Sprintf("%s/orgs/%s/repos?per_page=%d&type=all", c.baseURL, org, githubPerPage)
	return c.paginateRepos(ctx, token, url)
}

// ListUserRepos lists all repositories for a user.
func (c *GitHubClient) ListUserRepos(ctx context.Context, token, user string) ([]controller.RepoInfo, error) {
	url := fmt.Sprintf("%s/users/%s/repos?per_page=%d&type=all", c.baseURL, user, githubPerPage)
	return c.paginateRepos(ctx, token, url)
}

// GetRepo fetches a single repository's metadata.
func (c *GitHubClient) GetRepo(ctx context.Context, token, owner, repo string) (*controller.RepoInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, owner, repo)

	body, err := c.doRequest(ctx, token, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var ghRepo githubRepo
	if err := json.NewDecoder(body).Decode(&ghRepo); err != nil {
		return nil, fmt.Errorf("decoding github repo response: %w", err)
	}

	info := ghRepo.toRepoInfo()
	return &info, nil
}

// paginateRepos follows GitHub pagination (Link headers) to collect all repos.
func (c *GitHubClient) paginateRepos(ctx context.Context, token, initialURL string) ([]controller.RepoInfo, error) {
	var all []controller.RepoInfo
	url := initialURL

	for page := 0; url != "" && page < githubMaxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		c.setHeaders(req, token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github API request: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, c.handleErrorResponse(resp)
		}

		var repos []githubRepo
		if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decoding github repos response: %w", err)
		}
		resp.Body.Close()

		for _, r := range repos {
			all = append(all, r.toRepoInfo())
		}

		// Follow pagination
		url = parseNextLink(resp.Header.Get("Link"))
	}

	return all, nil
}

// doRequest performs a single GET request with auth headers.
func (c *GitHubClient) doRequest(ctx context.Context, token, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req, token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github API request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, c.handleErrorResponse(resp)
	}

	return resp.Body, nil
}

func (c *GitHubClient) setHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "agent-operator-core")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func (c *GitHubClient) handleErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("github: not found (404)")
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		return fmt.Errorf("github: rate limited (status %d, retry-after: %s)", resp.StatusCode, retryAfter)
	}
	return fmt.Errorf("github: unexpected status %d: %s", resp.StatusCode, string(body))
}

// parseNextLink extracts the "next" URL from a GitHub Link header.
// Format: <https://api.github.com/...?page=2>; rel="next", <...>; rel="last"
var linkNextRE = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func parseNextLink(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	matches := linkNextRE.FindStringSubmatch(linkHeader)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}
