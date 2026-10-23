package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/auth"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/detect"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/fixpr"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/github"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/redact"
)

const (
	maxManifestFiles   = 150
	maxManifestBytes   = 512 << 10
	manifestFetchLimit = 4
	fixPRTimeout       = 90 * time.Second
)

// fixPRsEnabled: accounts with repository access, somewhere to store
// links, and the relay (fix PRs are for connected clusters).
func (s *Server) fixPRsEnabled() bool {
	return s.Hub != nil && s.Auth != nil && s.Auth.RepoAccessAvailable() && s.RepoLinks != nil
}

// githubFor returns a GitHub client acting as the signed-in user.
func (s *Server) githubFor(ctx context.Context, w http.ResponseWriter, r *http.Request) (*github.Client, *model.User, bool) {
	u := userFrom(r.Context())
	if u == nil {
		writeError(w, http.StatusForbidden, "user_required", "fix pull requests are opened as a signed-in GitHub user")
		return nil, nil, false
	}
	tok, _, err := s.Auth.GitHubToken(ctx, u.ID)
	if errors.Is(err, auth.ErrNoRepoAccess) {
		writeError(w, http.StatusConflict, "connect_github", "connect GitHub repository access first")
		return nil, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "github_token", err.Error())
		return nil, nil, false
	}
	return github.New(s.Auth.GitHubAPI(), tok, s.Auth.HTTPClient()), u, true
}

// --- repository link ---

type repoLinkRequest struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

func (s *Server) handleGetRepoLink(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	l, err := s.RepoLinks.GetRepoLink(r.Context(), clusterID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "repo_link", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"link": l})
}

func (s *Server) handlePutRepoLink(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	var req repoLinkRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not parse request body")
		return
	}
	req.Repo = strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(req.Repo, "https://github.com/"), "github.com/")), "/")
	req.Repo = strings.TrimSuffix(req.Repo, ".git")
	if !github.ValidRepo(req.Repo) {
		writeError(w, http.StatusBadRequest, "invalid_repo", "repository must be owner/name")
		return
	}
	cleanPath, ok := cleanRepoPath(req.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_path", "folder must be a relative path inside the repository")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	gh, _, ok := s.githubFor(ctx, w, r)
	if !ok {
		return
	}
	repo, err := gh.Repo(ctx, req.Repo)
	switch {
	case errors.Is(err, github.ErrNotFound):
		writeError(w, http.StatusNotFound, "repo_not_found", "repository not found, or your connected access can't see it (private repositories need \"include private repositories\")")
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, "github_error", err.Error())
		return
	case !repo.Permissions.Push:
		writeError(w, http.StatusForbidden, "no_push_access", "you need push access to this repository to open pull requests")
		return
	case repo.Archived:
		writeError(w, http.StatusBadRequest, "repo_archived", "this repository is archived")
		return
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = repo.DefaultBranch
	}
	if _, err := gh.BranchSHA(ctx, req.Repo, branch); err != nil {
		writeError(w, http.StatusBadRequest, "branch_not_found", fmt.Sprintf("branch %q not found in %s", branch, req.Repo))
		return
	}
	link := fixpr.RepoLink{ClusterID: clusterID, Repo: repo.FullName, Branch: branch, Path: cleanPath}
	if err := s.RepoLinks.PutRepoLink(r.Context(), link); err != nil {
		writeError(w, http.StatusInternalServerError, "repo_link", err.Error())
		return
	}
	saved, _ := s.RepoLinks.GetRepoLink(r.Context(), clusterID)
	writeJSON(w, http.StatusOK, map[string]any{"link": saved})
}

func (s *Server) handleDeleteRepoLink(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	if err := s.RepoLinks.DeleteRepoLink(r.Context(), clusterID); err != nil {
		writeError(w, http.StatusInternalServerError, "repo_link", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cleanRepoPath normalizes a folder inside a repo ("" = root).
func cleanRepoPath(p string) (string, bool) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" || p == "." {
		return "", true
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") || strings.Contains(c, "\\") || len(c) > 200 {
		return "", false
	}
	return c, true
}

// --- fix options and pull requests ---

// diagnoseForFix collects the pod and computes the fix on the server:
// options are never built from anything the browser sends.
func (s *Server) diagnoseForFix(ctx context.Context, src clusterSource, ns, name string) (*model.DiagnosticSnapshot, error) {
	snap, err := src.CollectCore(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	core := detect.Detect(snap)
	snap.Detector = core
	if needsRelatedCheck(core, nil) {
		if related, err := src.CollectRelated(ctx, ns, name, nil); err == nil {
			snap.Related = related
			snap.Detector = detect.DetectRelated(snap, core)
		}
	}
	detect.Enrich(snap)
	redact.Snapshot(snap)
	return snap, nil
}

func (s *Server) handleFixOptions(w http.ResponseWriter, r *http.Request, src clusterSource, clusterID string) {
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	snap, err := s.diagnoseForFix(ctx, src, r.PathValue("ns"), r.PathValue("name"))
	if err != nil {
		writeSourceError(w, err, "pod_not_found")
		return
	}
	link, _ := s.RepoLinks.GetRepoLink(r.Context(), clusterID)
	opts := fixpr.Options(snap)
	if opts == nil {
		opts = []fixpr.Option{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"failureType": snap.Detector.FailureType, "options": opts, "link": link})
}

type fixPRRequest struct {
	OptionID string            `json:"optionId"`
	Inputs   map[string]string `json:"inputs"`
}

type fixPRResponse struct {
	URL     string   `json:"url"`
	Number  int      `json:"number"`
	Branch  string   `json:"branch"`
	File    string   `json:"file"`
	Changes []string `json:"changes"`
}

// handleOpenFixPR: re-diagnose, find the manifest in the linked repo,
// apply the chosen fix, push a branch and open a pull request. The
// default branch is never written to.
func (s *Server) handleOpenFixPR(w http.ResponseWriter, r *http.Request, src clusterSource, clusterID string) {
	var req fixPRRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not parse request body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fixPRTimeout)
	defer cancel()
	ns, name := r.PathValue("ns"), r.PathValue("name")

	link, err := s.RepoLinks.GetRepoLink(ctx, clusterID)
	if err != nil || link == nil {
		writeError(w, http.StatusConflict, "repo_not_linked", "link this cluster to a GitHub repository first")
		return
	}
	gh, user, ok := s.githubFor(ctx, w, r)
	if !ok {
		return
	}
	snap, err := s.diagnoseForFix(ctx, src, ns, name)
	if err != nil {
		writeSourceError(w, err, "pod_not_found")
		return
	}
	var opt *fixpr.Option
	for _, o := range fixpr.Options(snap) {
		if o.ID == req.OptionID {
			o := o
			opt = &o
		}
	}
	if opt == nil {
		writeError(w, http.StatusConflict, "fix_not_available", "that fix no longer applies to this pod (its state changed); reload the options")
		return
	}
	if opt.Unavailable != "" {
		writeError(w, http.StatusConflict, "fix_not_available", opt.Unavailable)
		return
	}

	baseSHA, err := gh.BranchSHA(ctx, link.Repo, link.Branch)
	if err != nil {
		writeError(w, http.StatusBadGateway, "github_error", "reading branch "+link.Branch+": "+err.Error())
		return
	}
	file, blobSHA, edited, changes, err := findAndEdit(ctx, gh, link, baseSHA, *opt, req.Inputs)
	if err != nil {
		var ue userError
		if errors.As(err, &ue) {
			writeError(w, ue.status, ue.code, ue.msg)
			return
		}
		writeError(w, http.StatusBadGateway, "github_error", err.Error())
		return
	}

	branch := fmt.Sprintf("kubeassist/%s-%s-%s", opt.ID, opt.Target.Name, randomSuffix())
	if err := gh.CreateBranch(ctx, link.Repo, branch, baseSHA); err != nil {
		writeError(w, http.StatusBadGateway, "github_error", "creating branch: "+err.Error())
		return
	}
	title := opt.Title
	if err := gh.UpdateFile(ctx, link.Repo, file, branch, title+"\n\nOpened from KubeAssist.", edited, blobSHA); err != nil {
		writeError(w, http.StatusBadGateway, "github_error", "committing "+file+": "+err.Error())
		return
	}
	pr, err := gh.CreatePull(ctx, link.Repo, branch, link.Branch, title, prBody(snap, *opt, file, changes, user.Login, clusterID))
	if err != nil {
		writeError(w, http.StatusBadGateway, "github_error", "opening the pull request: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, fixPRResponse{URL: pr.URL, Number: pr.Number, Branch: branch, File: file, Changes: changes})
}

// userError is a problem the user can fix (as opposed to GitHub failing).
type userError struct {
	status    int
	code, msg string
}

func (e userError) Error() string { return e.msg }

// findAndEdit searches the linked folder's YAML files for the target
// manifest and applies the option to it. Exactly one file must match.
func findAndEdit(ctx context.Context, gh *github.Client, link *fixpr.RepoLink, sha string, opt fixpr.Option, inputs map[string]string) (string, string, []byte, []string, error) {
	tree, truncated, err := gh.Tree(ctx, link.Repo, sha)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("listing files: %w", err)
	}
	var candidates []string
	for _, f := range tree {
		ext := strings.ToLower(path.Ext(f.Path))
		inFolder := link.Path == "" || strings.HasPrefix(f.Path, link.Path+"/")
		if inFolder && (ext == ".yaml" || ext == ".yml") && f.Size <= maxManifestBytes {
			candidates = append(candidates, f.Path)
		}
	}
	sort.Strings(candidates)
	where := link.Repo
	if link.Path != "" {
		where += "/" + link.Path
	}
	if len(candidates) == 0 {
		return "", "", nil, nil, userError{http.StatusNotFound, "manifest_not_found", "no YAML files in " + where}
	}
	if len(candidates) > maxManifestFiles {
		return "", "", nil, nil, userError{http.StatusBadRequest, "too_many_files",
			fmt.Sprintf("%s has %d YAML files; link a narrower folder (at most %d)", where, len(candidates), maxManifestFiles)}
	}

	type hit struct {
		path, sha string
		out       []byte
		changes   []string
	}
	var (
		mu       sync.Mutex
		hits     []hit
		editErrs []string
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(manifestFetchLimit)
	for _, p := range candidates {
		g.Go(func() error {
			content, blob, err := gh.File(gctx, link.Repo, p, sha)
			if err != nil {
				return fmt.Errorf("reading %s: %w", p, err)
			}
			out, changes, err := opt.Apply(content, inputs)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, fixpr.ErrNoMatch):
			case err != nil:
				editErrs = append(editErrs, p+": "+err.Error())
			default:
				hits = append(hits, hit{p, blob, out, changes})
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return "", "", nil, nil, err
	}
	if len(editErrs) > 0 {
		sort.Strings(editErrs)
		return "", "", nil, nil, userError{http.StatusUnprocessableEntity, "edit_failed", editErrs[0]}
	}
	switch len(hits) {
	case 0:
		msg := fmt.Sprintf("no manifest for %s %q (namespace %s) in %s", opt.Target.Kind, opt.Target.Name, opt.Target.Namespace, where)
		if truncated {
			msg += " (GitHub truncated the file list; link a narrower folder)"
		}
		return "", "", nil, nil, userError{http.StatusNotFound, "manifest_not_found", msg}
	case 1:
		h := hits[0]
		return h.path, h.sha, h.out, h.changes, nil
	}
	paths := make([]string, len(hits))
	for i, h := range hits {
		paths[i] = h.path
	}
	sort.Strings(paths)
	return "", "", nil, nil, userError{http.StatusConflict, "ambiguous_manifest",
		fmt.Sprintf("%s %q is defined in several files (%s); link a narrower folder", opt.Target.Kind, opt.Target.Name, strings.Join(paths, ", "))}
}

func prBody(snap *model.DiagnosticSnapshot, opt fixpr.Option, file string, changes []string, login, clusterID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", opt.Title)
	fmt.Fprintf(&b, "KubeAssist diagnosed pod `%s/%s` in cluster `%s` as **%s**.\n\n", snap.Pod.Namespace, snap.Pod.Name, clusterID, snap.Detector.FailureType)
	if ev := snap.Detector.Evidence; len(ev) > 0 {
		b.WriteString("### Evidence\n\n")
		for _, e := range firstN(ev, 5) {
			fmt.Fprintf(&b, "- %s\n", e)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "### Change in `%s`\n\n", file)
	for _, c := range changes {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	fmt.Fprintf(&b, "\n%s\n\n", opt.Description)
	b.WriteString("### After merging\n\nDeploy as usual, then use **Did the fix work?** in KubeAssist (or `kubectl get pods -n " + snap.Pod.Namespace + " -w`) to confirm.\n\n")
	fmt.Fprintf(&b, "---\n_Opened by KubeAssist for @%s. Nothing was applied to the cluster; review before merging._\n", login)
	return b.String()
}

func randomSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
