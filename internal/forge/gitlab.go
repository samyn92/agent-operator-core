package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/samyn92/agent-operator-core/internal/controller"
)

// Compile-time interface check.
var _ controller.GitLabAPIClient = (*GitLabClient)(nil)

const (
	// GitLab paginates at 100 per page max.
	gitlabPerPage = 100
	// Maximum pages to fetch to prevent runaway pagination.
	gitlabMaxPages = 50
)

// GitLabClient implements the GitLabAPIClient interface using the GitLab REST API v4.
type GitLabClient struct {
	httpClient *http.Client
}

// GitLabOption configures a GitLabClient.
type GitLabOption func(*GitLabClient)

// WithGitLabHTTPClient sets a custom HTTP client.
func WithGitLabHTTPClient(client *http.Client) GitLabOption {
	return func(c *GitLabClient) {
		c.httpClient = client
	}
}

// NewGitLabClient creates a new GitLab API client.
func NewGitLabClient(opts ...GitLabOption) *GitLabClient {
	c := &GitLabClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// gitlabProject is the JSON structure returned by GitLab's projects API.
type gitlabProject struct {
	PathWithNamespace string   `json:"path_with_namespace"`
	HTTPURLToRepo     string   `json:"http_url_to_repo"`
	SSHURLToRepo      string   `json:"ssh_url_to_repo"`
	DefaultBranch     string   `json:"default_branch"`
	Description       string   `json:"description"`
	Visibility        string   `json:"visibility"` // "public", "internal", "private"
	Archived          bool     `json:"archived"`
	LastActivityAt    string   `json:"last_activity_at"`
	Topics            []string `json:"topics"`
	// Older GitLab versions use tag_list instead of topics
	TagList []string `json:"tag_list"`
}

func (p *gitlabProject) toRepoInfo() controller.RepoInfo {
	info := controller.RepoInfo{
		FullName:      p.PathWithNamespace,
		CloneURL:      p.HTTPURLToRepo,
		SSHURL:        p.SSHURLToRepo,
		DefaultBranch: p.DefaultBranch,
		Description:   p.Description,
		Visibility:    p.Visibility,
		Archived:      p.Archived,
	}
	// Prefer topics, fall back to tag_list
	if len(p.Topics) > 0 {
		info.Topics = p.Topics
	} else if len(p.TagList) > 0 {
		info.Topics = p.TagList
	}
	if t, err := time.Parse(time.RFC3339, p.LastActivityAt); err == nil {
		info.LastActivity = t
	}
	return info
}

// apiBaseURL constructs the API base URL for a given domain.
// Supports custom GitLab instances (e.g., gitlab.example.com).
func apiBaseURL(domain string) string {
	if domain == "" {
		domain = "gitlab.com"
	}
	return fmt.Sprintf("https://%s/api/v4", domain)
}

// ListGroupProjects lists all projects in a GitLab group.
// If recursive is true, includes projects from subgroups.
func (c *GitLabClient) ListGroupProjects(ctx context.Context, token, domain, group string, recursive bool) ([]controller.RepoInfo, error) {
	base := apiBaseURL(domain)
	// Group path must be URL-encoded (e.g., "org/platform" -> "org%2Fplatform")
	encodedGroup := url.PathEscape(group)

	includeSubgroups := "false"
	if recursive {
		includeSubgroups = "true"
	}

	apiURL := fmt.Sprintf("%s/groups/%s/projects?per_page=%d&include_subgroups=%s&with_shared=false&archived=false",
		base, encodedGroup, gitlabPerPage, includeSubgroups)

	return c.paginateProjects(ctx, token, apiURL)
}

// ListUserProjects lists all projects for a GitLab user.
func (c *GitLabClient) ListUserProjects(ctx context.Context, token, domain, user string) ([]controller.RepoInfo, error) {
	base := apiBaseURL(domain)
	apiURL := fmt.Sprintf("%s/users/%s/projects?per_page=%d", base, user, gitlabPerPage)
	return c.paginateProjects(ctx, token, apiURL)
}

// GetProject fetches a single project's metadata by path (e.g., "org/platform/api").
func (c *GitLabClient) GetProject(ctx context.Context, token, domain, projectPath string) (*controller.RepoInfo, error) {
	base := apiBaseURL(domain)
	// Project path must be URL-encoded
	encodedPath := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/projects/%s", base, encodedPath)

	body, err := c.doRequest(ctx, token, apiURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var project gitlabProject
	if err := json.NewDecoder(body).Decode(&project); err != nil {
		return nil, fmt.Errorf("decoding gitlab project response: %w", err)
	}

	info := project.toRepoInfo()
	return &info, nil
}

// paginateProjects follows GitLab pagination (page parameter) to collect all projects.
// GitLab uses page-based pagination with X-Next-Page headers.
func (c *GitLabClient) paginateProjects(ctx context.Context, token, initialURL string) ([]controller.RepoInfo, error) {
	var all []controller.RepoInfo
	apiURL := initialURL

	for page := 0; page < gitlabMaxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		c.setHeaders(req, token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("gitlab API request: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, c.handleErrorResponse(resp)
		}

		var projects []gitlabProject
		if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decoding gitlab projects response: %w", err)
		}
		resp.Body.Close()

		for _, p := range projects {
			all = append(all, p.toRepoInfo())
		}

		// Check for next page
		nextPage := resp.Header.Get("X-Next-Page")
		if nextPage == "" || nextPage == "0" {
			break
		}

		// Build URL for next page
		parsedURL, err := url.Parse(apiURL)
		if err != nil {
			break
		}
		q := parsedURL.Query()
		q.Set("page", nextPage)
		parsedURL.RawQuery = q.Encode()
		apiURL = parsedURL.String()
	}

	return all, nil
}

// doRequest performs a single GET request with auth headers.
func (c *GitLabClient) doRequest(ctx context.Context, token, apiURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req, token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab API request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, c.handleErrorResponse(resp)
	}

	return resp.Body, nil
}

func (c *GitLabClient) setHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agent-operator-core")
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
}

func (c *GitLabClient) handleErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("gitlab: not found (404)")
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		return fmt.Errorf("gitlab: rate limited (status %d, retry-after: %s)", resp.StatusCode, retryAfter)
	}
	// Handle unauthorized
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("gitlab: unauthorized (401) — check your access token")
	}
	return fmt.Errorf("gitlab: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
