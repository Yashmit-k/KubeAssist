// Package github is a minimal GitHub REST client for fix pull requests:
// read a repository's tree and files, create a branch, commit one file
// and open a pull request — with the user's own OAuth token.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultAPI is github.com's API root.
const DefaultAPI = "https://api.github.com"

// ErrNotFound is a 404 from GitHub (also what it returns for private
// repositories the token can't see).
var ErrNotFound = errors.New("not found on GitHub")

// repoPattern is "owner/name" as GitHub allows them.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

// ValidRepo reports whether s looks like "owner/name".
func ValidRepo(s string) bool { return repoPattern.MatchString(s) && !strings.Contains(s, "..") }

// Client calls the GitHub API as one user.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New builds a client; base "" means github.com.
func New(base, token string, hc *http.Client) *Client {
	if base == "" {
		base = DefaultAPI
	}
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: hc}
}

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("GitHub returned %d: %s", e.Status, e.Message) }

func (c *Client) do(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling GitHub: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusNotFound {
		return resp.Header, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(raw))
		}
		return resp.Header, &APIError{Status: resp.StatusCode, Message: truncate(e.Message, 200)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.Header, fmt.Errorf("parsing GitHub response: %w", err)
		}
	}
	return resp.Header, nil
}

// User is the authenticated user; Scopes comes from X-OAuth-Scopes.
type User struct {
	ID     int64  `json:"id"`
	Login  string `json:"login"`
	Scopes string `json:"-"`
}

func (c *Client) User(ctx context.Context) (*User, error) {
	var u User
	h, err := c.do(ctx, http.MethodGet, "/user", nil, &u)
	if err != nil {
		return nil, err
	}
	u.Scopes = h.Get("X-OAuth-Scopes")
	return &u, nil
}

// Repo is the subset of repository metadata fix PRs use.
type Repo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Permissions   struct {
		Push bool `json:"push"`
	} `json:"permissions"`
}

func (c *Client) Repo(ctx context.Context, repo string) (*Repo, error) {
	var r Repo
	if _, err := c.do(ctx, http.MethodGet, "/repos/"+repo, nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// BranchSHA returns the commit a branch points at.
func (c *Client) BranchSHA(ctx context.Context, repo, branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/git/ref/heads/"+url.PathEscape(branch), nil, &ref); err != nil {
		return "", err
	}
	return ref.Object.SHA, nil
}

// TreeFile is one blob in a tree listing.
type TreeFile struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int    `json:"size"`
}

// Tree lists every file at a commit. Truncated is GitHub saying the
// tree was too large to list in one call.
func (c *Client) Tree(ctx context.Context, repo, sha string) ([]TreeFile, bool, error) {
	var t struct {
		Tree      []TreeFile `json:"tree"`
		Truncated bool       `json:"truncated"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/git/trees/"+sha+"?recursive=1", nil, &t); err != nil {
		return nil, false, err
	}
	files := t.Tree[:0]
	for _, f := range t.Tree {
		if f.Type == "blob" {
			files = append(files, f)
		}
	}
	return files, t.Truncated, nil
}

// File returns a file's content and blob SHA at a ref.
func (c *Client) File(ctx context.Context, repo, path, ref string) (content []byte, sha string, err error) {
	var f struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/contents/"+escapePath(path)+"?ref="+url.QueryEscape(ref), nil, &f); err != nil {
		return nil, "", err
	}
	if f.Encoding != "base64" {
		return nil, "", fmt.Errorf("unexpected encoding %q for %s", f.Encoding, path)
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
	return b, f.SHA, err
}

// CreateBranch points a new branch at sha.
func (c *Client) CreateBranch(ctx context.Context, repo, branch, sha string) error {
	_, err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": sha}, nil)
	return err
}

// UpdateFile commits new content for an existing file on branch.
func (c *Client) UpdateFile(ctx context.Context, repo, path, branch, message string, content []byte, blobSHA string) error {
	_, err := c.do(ctx, http.MethodPut, "/repos/"+repo+"/contents/"+escapePath(path), map[string]string{
		"message": message, "content": base64.StdEncoding.EncodeToString(content), "sha": blobSHA, "branch": branch,
	}, nil)
	return err
}

// PullRequest is a created PR.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
}

func (c *Client) CreatePull(ctx context.Context, repo, head, base, title, body string) (*PullRequest, error) {
	var pr PullRequest
	_, err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/pulls", map[string]any{
		"title": title, "head": head, "base": base, "body": body, "maintainer_can_modify": true,
	}, &pr)
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

func escapePath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
