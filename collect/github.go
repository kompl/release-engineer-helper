package collect

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHubClient provides methods to interact with GitHub API.
type GitHubClient struct {
	token    string
	owner    string
	workflow string
	client   *http.Client
}

// NewGitHubClient creates a new GitHub API client.
func NewGitHubClient(token, owner, workflowFile string) *GitHubClient {
	return &GitHubClient{
		token:    token,
		owner:    owner,
		workflow: workflowFile,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// get performs a GET request to the GitHub API with JSON accept header.
// Retries up to 3 times on rate limit (HTTP 403 with X-RateLimit-Remaining: 0).
func (g *GitHubClient) get(url string) ([]byte, error) {
	const maxRetries = 3

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", "token "+g.token)
		req.Header.Set("Accept", "application/vnd.github.v3+json")

		resp, err := g.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("do request: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}

		if resp.StatusCode == 403 && resp.Header.Get("X-RateLimit-Remaining") == "0" && attempt < maxRetries {
			resetStr := resp.Header.Get("X-RateLimit-Reset")
			sleepDur := time.Duration(30*(attempt+1)) * time.Second
			if resetStr != "" {
				if resetUnix, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
					resetTime := time.Unix(resetUnix, 0)
					if wait := time.Until(resetTime); wait > 0 && wait < 15*time.Minute {
						sleepDur = wait + time.Second
					}
				}
			}
			log.Printf("[github] Rate limited, waiting %v before retry %d/%d", sleepDur, attempt+1, maxRetries)
			time.Sleep(sleepDur)
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
		}

		return body, nil
	}

	return nil, fmt.Errorf("exhausted retries for %s", url)
}

// getZip performs a GET request for binary content (zip downloads).
// Tries standard accept header first, falls back to octet-stream on 415/406.
func (g *GitHubClient) getZip(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "token "+g.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 415 || resp.StatusCode == 406 {
		io.Copy(io.Discard, resp.Body) // drain body for connection reuse
		// Retry with octet-stream
		req2, _ := http.NewRequest("GET", url, nil)
		req2.Header.Set("Authorization", "token "+g.token)
		req2.Header.Set("Accept", "application/octet-stream")

		resp2, err := g.client.Do(req2)
		if err != nil {
			return nil, fmt.Errorf("retry request: %w", err)
		}
		defer resp2.Body.Close()

		if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d on retry", resp2.StatusCode)
		}
		return io.ReadAll(resp2.Body)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	return io.ReadAll(resp.Body)
}

// FetchRunsPage fetches a single page of workflow runs.
// Empty branch fetches runs across all branches.
func (g *GitHubClient) FetchRunsPage(repo, branch string, page int) ([]ghWorkflowRun, error) {
	branchParam := ""
	if branch != "" {
		branchParam = "branch=" + url.QueryEscape(branch) + "&"
	}
	reqURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/actions/workflows/%s/runs?%sper_page=100&page=%d",
		g.owner, repo, g.workflow, branchParam, page,
	)

	body, err := g.get(reqURL)
	if err != nil {
		return nil, err
	}

	var resp ghWorkflowRunsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse runs response: %w", err)
	}

	return resp.WorkflowRuns, nil
}

// GetRun fetches a single workflow run by ID.
func (g *GitHubClient) GetRun(repo string, runID int) (*ghWorkflowRun, error) {
	reqURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs/%d", g.owner, repo, runID)

	body, err := g.get(reqURL)
	if err != nil {
		return nil, err
	}

	var run ghWorkflowRun
	if err := json.Unmarshal(body, &run); err != nil {
		return nil, fmt.Errorf("parse run response: %w", err)
	}
	return &run, nil
}

// GetBranchHeadSHA returns the newest commit SHA on a branch at or before
// the given RFC3339 time.
func (g *GitHubClient) GetBranchHeadSHA(repo, branch, until string) (string, error) {
	reqURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/commits?sha=%s&until=%s&per_page=1",
		g.owner, repo, url.QueryEscape(branch), url.QueryEscape(until),
	)

	body, err := g.get(reqURL)
	if err != nil {
		return "", err
	}

	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &commits); err != nil {
		return "", fmt.Errorf("parse commits response: %w", err)
	}
	if len(commits) == 0 {
		return "", fmt.Errorf("no commits on %s/%s before %s", repo, branch, until)
	}
	return commits[0].SHA, nil
}

// GetCommitTitle fetches the first line of a commit message by SHA.
func (g *GitHubClient) GetCommitTitle(repo, sha string) string {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", g.owner, repo, sha)

	body, err := g.get(url)
	if err != nil {
		return sha[:min(len(sha), 7)]
	}

	var resp ghCommitResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return sha[:min(len(sha), 7)]
	}

	msg := resp.Commit.Message
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		msg = msg[:idx]
	}
	if msg == "" {
		return sha[:min(len(sha), 7)]
	}
	return msg
}

// DownloadLogs downloads the zip of workflow run logs.
func (g *GitHubClient) DownloadLogs(repo string, runID int) ([]byte, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs/%d/logs", g.owner, repo, runID)
	return g.get(url)
}

// ListRunArtifacts lists artifacts for a workflow run.
func (g *GitHubClient) ListRunArtifacts(repo string, runID int) ([]ghArtifact, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs/%d/artifacts", g.owner, repo, runID)

	body, err := g.get(url)
	if err != nil {
		return nil, err
	}

	var resp ghArtifactsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse artifacts: %w", err)
	}
	return resp.Artifacts, nil
}

// DownloadArtifact downloads a zip of an artifact by its download URL.
func (g *GitHubClient) DownloadArtifact(downloadURL string) ([]byte, error) {
	return g.getZip(downloadURL)
}

// RunURL builds the HTML URL for a workflow run.
func (g *GitHubClient) RunURL(repo string, runID int) string {
	return fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", g.owner, repo, runID)
}
