package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/auth"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/collector"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/fixpr"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// fakeRepo is a GitHub repository "o/r" whose files are real lab manifests.
type fakeRepo struct {
	mu      sync.Mutex
	files   map[string][]byte
	refs    []string // branches created
	puts    map[string][]byte
	putRefs []string // branch of each commit
	pulls   []map[string]any
}

func newFakeRepo(t *testing.T, files map[string][]byte) (*fakeRepo, *httptest.Server) {
	t.Helper()
	f := &fakeRepo{files: files, puts: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"full_name":"o/r","default_branch":"main","permissions":{"push":true}}`))
	})
	mux.HandleFunc("GET /repos/o/r/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":{"sha":"base1"}}`))
	})
	mux.HandleFunc("GET /repos/o/r/git/trees/base1", func(w http.ResponseWriter, r *http.Request) {
		var tree []map[string]any
		for p, c := range f.files {
			tree = append(tree, map[string]any{"path": p, "type": "blob", "size": len(c)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tree": tree})
	})
	mux.HandleFunc("GET /repos/o/r/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		c, ok := f.files[r.PathValue("path")]
		if !ok || r.URL.Query().Get("ref") != "base1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"content": base64.StdEncoding.EncodeToString(c), "encoding": "base64", "sha": "blob-" + r.PathValue("path")})
	})
	mux.HandleFunc("POST /repos/o/r/git/refs", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.refs = append(f.refs, b["ref"]+"@"+b["sha"])
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("PUT /repos/o/r/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["sha"] != "blob-"+r.PathValue("path") {
			w.WriteHeader(http.StatusConflict)
			return
		}
		c, _ := base64.StdEncoding.DecodeString(b["content"])
		f.mu.Lock()
		f.puts[r.PathValue("path")], f.putRefs = c, append(f.putRefs, b["branch"])
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.pulls = append(f.pulls, b)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/o/r/pull/7"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func readLab(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "lab", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// oomCluster: Deployment oom-failure -> ReplicaSet -> OOMKilled pod.
func oomCluster() []runtime.Object {
	ns := "kubeassist-lab"
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "oom-failure", Namespace: ns}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "oom-failure-abc", Namespace: ns,
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "oom-failure"}}}}
	code := int32(137)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "oom-failure-abc-x1", Namespace: ns, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "oom-failure-abc"}}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "application", Image: "python:3.13-alpine",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "application", RestartCount: 4,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: code}},
		}}},
	}
	return []runtime.Object{dep, rs, pod}
}

type fixPRHarness struct {
	s    *Server
	src  clusterSource
	user *model.User
	repo *fakeRepo
}

func newFixPRHarness(t *testing.T, files map[string][]byte, withToken, withLink bool) *fixPRHarness {
	t.Helper()
	repo, gh := newFakeRepo(t, files)
	st := auth.NewMemoryStore()
	m, err := auth.NewManager(st, auth.Config{PublicURL: "http://ui.test",
		GitHub:   &auth.GitHubConfig{ClientID: "cid", ClientSecret: "s", APIBaseURL: gh.URL},
		TokenKey: []byte("0123456789abcdef0123456789abcdef"), TokenStore: st})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := st.UpsertUser(context.Background(), model.User{Provider: "github", ProviderUserID: "4242", Login: "octocat"})
	if withToken {
		if err := m.StoreGitHubToken(context.Background(), u.ID, "gho_repo", "public_repo"); err != nil {
			t.Fatal(err)
		}
	}
	links := fixpr.NewMemoryLinks()
	if withLink {
		_ = links.PutRepoLink(context.Background(), fixpr.RepoLink{ClusterID: "prod", Repo: "o/r", Branch: "main", Path: "deploy/lab"})
	}
	s := &Server{Auth: m, RepoLinks: links, TroubleshootTimeout: time.Second}
	return &fixPRHarness{s: s, src: collector.New(fake.NewSimpleClientset(oomCluster()...), "kind", "v1"), user: &u, repo: repo}
}

func (h *fixPRHarness) call(handler scopedHandler, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/remote/prod/pods/kubeassist-lab/oom-failure-abc-x1/fix-pr", strings.NewReader(body))
	req.SetPathValue("ns", "kubeassist-lab")
	req.SetPathValue("name", "oom-failure-abc-x1")
	req = req.WithContext(withUser(req.Context(), h.user))
	rec := httptest.NewRecorder()
	handler(rec, req, h.src, "prod")
	return rec
}

func TestFixPR_OpensAPullRequestWithAOneLineChange(t *testing.T) {
	oom := readLab(t, "oom-failure.yaml")
	files := map[string][]byte{
		"deploy/lab/oom-failure.yaml": oom,
		"deploy/lab/healthy-app.yaml": readLab(t, "healthy-app.yaml"),
		"other/oom-failure-copy.yaml": oom, // outside the linked folder: must be ignored
		"deploy/lab/README.md":        []byte("# not yaml"),
	}
	h := newFixPRHarness(t, files, true, true)

	rec := h.call(h.s.handleFixOptions, "GET", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"raise-memory"`) || !strings.Contains(rec.Body.String(), `"default":"64Mi"`) {
		t.Fatalf("options: %d %s", rec.Code, rec.Body)
	}

	rec = h.call(h.s.handleOpenFixPR, "POST", `{"optionId":"raise-memory"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("open PR: %d %s", rec.Code, rec.Body)
	}
	var out fixPRResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.URL != "https://github.com/o/r/pull/7" || out.File != "deploy/lab/oom-failure.yaml" || !strings.HasPrefix(out.Branch, "kubeassist/raise-memory-oom-failure-") {
		t.Errorf("response = %+v", out)
	}

	r := h.repo
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.refs) != 1 || r.refs[0] != "refs/heads/"+out.Branch+"@base1" {
		t.Errorf("branch must be created from main's head: %v", r.refs)
	}
	if len(r.puts) != 1 || len(r.putRefs) != 1 || r.putRefs[0] != out.Branch {
		t.Fatalf("exactly one commit, on the new branch (never main): %v", r.putRefs)
	}
	before, after := strings.Split(string(oom), "\n"), strings.Split(string(r.puts["deploy/lab/oom-failure.yaml"]), "\n")
	var diff []string
	for i := range before {
		if before[i] != after[i] {
			diff = append(diff, strings.TrimSpace(after[i]))
		}
	}
	if len(before) != len(after) || len(diff) != 1 || diff[0] != "memory: 64Mi" {
		t.Errorf("the PR must change exactly the limit line, diff = %q", diff)
	}
	pr := r.pulls[0]
	body := pr["body"].(string)
	if pr["base"] != "main" || pr["head"] != out.Branch || !strings.Contains(body, "OOMKilled") ||
		!strings.Contains(body, "resources.limits.memory = 64Mi") || !strings.Contains(body, "@octocat") || !strings.Contains(body, "Nothing was applied to the cluster") {
		t.Errorf("pull = %+v", pr)
	}
}

func TestFixPR_UserFacingErrors(t *testing.T) {
	oom := readLab(t, "oom-failure.yaml")
	one := map[string][]byte{"deploy/lab/oom-failure.yaml": oom}

	h := newFixPRHarness(t, one, true, false)
	if rec := h.call(h.s.handleOpenFixPR, "POST", `{"optionId":"raise-memory"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "repo_not_linked") {
		t.Errorf("no link: %d %s", rec.Code, rec.Body)
	}
	h = newFixPRHarness(t, one, false, true)
	if rec := h.call(h.s.handleOpenFixPR, "POST", `{"optionId":"raise-memory"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "connect_github") {
		t.Errorf("no token: %d %s", rec.Code, rec.Body)
	}
	h = newFixPRHarness(t, one, true, true)
	if rec := h.call(h.s.handleOpenFixPR, "POST", `{"optionId":"set-image"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "fix_not_available") {
		t.Errorf("option the pod doesn't have: %d %s", rec.Code, rec.Body)
	}
	if rec := h.call(h.s.handleOpenFixPR, "POST", `{"optionId":"raise-memory","inputs":{"memory":"lots"}}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not a Kubernetes quantity") {
		t.Errorf("bad input: %d %s", rec.Code, rec.Body)
	}
	h = newFixPRHarness(t, map[string][]byte{"deploy/lab/a.yaml": oom, "deploy/lab/b.yaml": oom}, true, true)
	if rec := h.call(h.s.handleOpenFixPR, "POST", `{"optionId":"raise-memory"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "several files") {
		t.Errorf("ambiguous: %d %s", rec.Code, rec.Body)
	}
	h = newFixPRHarness(t, map[string][]byte{"deploy/lab/healthy-app.yaml": readLab(t, "healthy-app.yaml")}, true, true)
	if rec := h.call(h.s.handleOpenFixPR, "POST", `{"optionId":"raise-memory"}`); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `no manifest for Deployment \"oom-failure\"`) {
		t.Errorf("not found: %d %s", rec.Code, rec.Body)
	}
	if len(h.repo.refs) != 0 {
		t.Error("nothing may be pushed when the PR can't be built")
	}
}

func TestCleanRepoPath(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "deploy/lab/": "deploy/lab", "./k8s": "k8s", "a//b": "a/b"} {
		if got, ok := cleanRepoPath(in); !ok || got != want {
			t.Errorf("cleanRepoPath(%q) = %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"../x", "a/../../b", "..", `a\b`} {
		if _, ok := cleanRepoPath(bad); ok {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestRepoLink_ValidatesAgainstGitHub(t *testing.T) {
	h := newFixPRHarness(t, map[string][]byte{}, true, false)
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/api/remote/prod/repo", strings.NewReader(body))
		req = req.WithContext(withUser(req.Context(), h.user))
		rec := httptest.NewRecorder()
		h.s.handlePutRepoLink(rec, req, nil, "prod")
		return rec
	}
	for body, code := range map[string]string{
		`{"repo":"not a repo"}`:                    "invalid_repo",
		`{"repo":"o/r","path":"../etc"}`:           "invalid_path",
		`{"repo":"o/missing"}`:                     "repo_not_found",
		`{"repo":"o/r","branch":"no-such-branch"}`: "branch_not_found",
	} {
		if rec := put(body); !strings.Contains(rec.Body.String(), code) {
			t.Errorf("%s: want %s, got %d %s", body, code, rec.Code, rec.Body)
		}
	}
	rec := put(`{"repo":"https://github.com/o/r.git","path":"/deploy/lab/"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("link: %d %s", rec.Code, rec.Body)
	}
	l, _ := h.s.RepoLinks.GetRepoLink(context.Background(), "prod")
	if l == nil || l.Repo != "o/r" || l.Branch != "main" || l.Path != "deploy/lab" {
		t.Errorf("link = %+v (URL form accepted, default branch used, path cleaned)", l)
	}
}
