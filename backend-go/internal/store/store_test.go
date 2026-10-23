package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/alerts"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/fixpr"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// These are integration tests against a real Postgres. They run when
// TEST_DATABASE_URL is set (CI starts a postgres service container):
//
//	docker run -d --rm -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL=postgres://postgres:test@localhost:55432/postgres?sslmode=disable go test ./internal/store/
func openTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration tests")
	}
	s, err := New(url, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	// Migrate twice: it must be idempotent.
	for i := 0; i < 2; i++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `TRUNCATE investigations, alert_configs, cluster_repos, github_tokens, clusters, sessions, users;`); err != nil {
		t.Fatal(err)
	}
	return s
}

func diagnosis(ft string) model.DiagnosisResponse {
	return model.DiagnosisResponse{
		SchemaVersion: "1.1.0", FailureType: ft, IssueSummary: "summary " + ft,
		ObservedEvidence: []string{"e"}, ProbableRootCause: "r", Reasoning: "x",
		ConfidenceScore: 0.87, ResolutionSteps: []model.ResolutionStep{{Order: 1, Description: "d"}},
		VerificationCommands: []string{}, DocumentationReferences: []model.DocumentationRef{}, UncertaintyNotes: []string{},
		Mode: "mock_llm",
	}
}

func TestStore_SaveGetListScopedByCluster(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	snap := model.DiagnosticSnapshot{SchemaVersion: "1.1.0", Pod: model.PodInfo{Name: "api-1", Namespace: "shop", Phase: "Running"}}

	localID, err := s.Save(ctx, LocalCluster, "shop", "api-1", snap, diagnosis("CrashLoopBackOff"))
	if err != nil {
		t.Fatal(err)
	}
	remoteID, err := s.Save(ctx, "customer-a", "shop", "api-1", snap, diagnosis("OOMKilled"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, LocalCluster, localID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Diagnosis.FailureType != "CrashLoopBackOff" || got.Snapshot.Pod.Name != "api-1" || got.ClusterID != LocalCluster {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Cross-cluster reads look exactly like "not found".
	if _, err := s.Get(ctx, LocalCluster, remoteID); !errors.Is(err, ErrNotFound) {
		t.Errorf("local scope must not see a remote investigation, got %v", err)
	}
	if _, err := s.Get(ctx, LocalCluster, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("malformed id should be not found, got %v", err)
	}

	list, err := s.List(ctx, "customer-a", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != remoteID || list[0].FailureType != "OOMKilled" || list[0].ConfidenceScore != 0.87 {
		t.Errorf("list = %+v", list)
	}
	if list, _ := s.List(ctx, LocalCluster, "other-ns", 10); len(list) != 0 {
		t.Errorf("namespace filter failed: %+v", list)
	}
}

func TestStore_ClusterRegistrations(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.UpsertUser(ctx, model.User{Provider: "github", ProviderUserID: "1", Login: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCluster(ctx, "prod", "hash-1", owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCluster(ctx, "seeded", "hash-s", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCluster(ctx, "prod", "hash-2", ""); err == nil {
		t.Error("duplicate cluster id must fail")
	}
	snap := model.DiagnosticSnapshot{SchemaVersion: "1.1.0"}
	if _, err := s.Save(ctx, "prod", "ns", "p", snap, diagnosis("Healthy")); err != nil {
		t.Fatal(err)
	}

	all, err := s.LoadClusters(ctx)
	if err != nil || all["prod"].TokenHash != "hash-1" || all["prod"].OwnerID != owner.ID || all["seeded"].OwnerID != "" {
		t.Fatalf("LoadClusters = %v, %v", all, err)
	}
	if err := s.DeleteCluster(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	all, _ = s.LoadClusters(ctx)
	list, _ := s.List(ctx, "prod", "", 10)
	if _, ok := all["prod"]; ok || len(list) != 0 {
		t.Errorf("delete should remove registration and history: %v %v", all, list)
	}
}

func TestStore_UsersAndSessions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	u, err := s.UpsertUser(ctx, model.User{Provider: "github", ProviderUserID: "4242", Login: "octocat", Name: "Octo"})
	if err != nil || u.ID == "" {
		t.Fatalf("upsert: %+v %v", u, err)
	}
	// Same identity, renamed login -> same row, refreshed profile.
	again, err := s.UpsertUser(ctx, model.User{Provider: "github", ProviderUserID: "4242", Login: "octo-renamed"})
	if err != nil || again.ID != u.ID {
		t.Fatalf("re-upsert should keep the ID: %+v %v", again, err)
	}
	other, _ := s.UpsertUser(ctx, model.User{Provider: "dev", ProviderUserID: "4242", Login: "4242"})
	if other.ID == u.ID {
		t.Error("identities are per provider")
	}

	if err := s.CreateSession(ctx, "live", u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "expired", u.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionUser(ctx, "live")
	if err != nil || got == nil || got.ID != u.ID || got.Login != "octo-renamed" || got.ProviderUserID != "4242" {
		t.Fatalf("SessionUser(live) = %+v, %v", got, err)
	}
	for _, h := range []string{"expired", "missing"} {
		if got, err := s.SessionUser(ctx, h); got != nil || err != nil {
			t.Errorf("SessionUser(%s) = %+v, %v; want nil, nil", h, got, err)
		}
	}
	if err := s.DeleteSession(ctx, "live"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SessionUser(ctx, "live"); got != nil {
		t.Error("deleted session must not resolve")
	}
}

func TestStore_AlertConfigs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, model.User{Provider: "dev", ProviderUserID: "a", Login: "a"})
	if err := s.SaveCluster(ctx, "prod", "h", u.ID); err != nil {
		t.Fatal(err)
	}
	if c, err := s.GetAlertConfig(ctx, "prod"); c != nil || err != nil {
		t.Fatalf("no config yet: %+v %v", c, err)
	}
	cfg := alerts.Config{ClusterID: "prod", OwnerID: u.ID, Kind: alerts.KindSlack, URL: "https://hooks.slack.com/x", Enabled: true}
	if err := s.PutAlertConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Kind, cfg.Enabled = alerts.KindDiscord, false
	if err := s.PutAlertConfig(ctx, cfg); err != nil { // upsert
		t.Fatal(err)
	}
	got, err := s.GetAlertConfig(ctx, "prod")
	if err != nil || got == nil || got.Kind != alerts.KindDiscord || got.Enabled || got.OwnerID != u.ID || got.UpdatedAt.IsZero() {
		t.Fatalf("got %+v %v", got, err)
	}
	if all, _ := s.ListAlertConfigs(ctx); len(all) != 1 {
		t.Errorf("list = %+v", all)
	}
	// Revoking the cluster removes its alert config with it.
	if err := s.DeleteCluster(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetAlertConfig(ctx, "prod"); c != nil {
		t.Errorf("config must cascade with the cluster: %+v", c)
	}
	if err := s.PutAlertConfig(ctx, alerts.Config{ClusterID: "ghost", OwnerID: u.ID, Kind: "slack", URL: "x"}); err == nil {
		t.Error("a config for an unregistered cluster must be rejected")
	}
}

func TestStore_GitHubTokensAndRepoLinks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, model.User{Provider: "github", ProviderUserID: "7", Login: "o"})
	if sealed, _, err := s.GitHubToken(ctx, u.ID); sealed != nil || err != nil {
		t.Fatalf("none yet: %v %v", sealed, err)
	}
	if err := s.SaveGitHubToken(ctx, u.ID, []byte{1, 2, 3}, "public_repo"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGitHubToken(ctx, u.ID, []byte{4, 5}, "repo"); err != nil { // replace
		t.Fatal(err)
	}
	sealed, scopes, err := s.GitHubToken(ctx, u.ID)
	if err != nil || string(sealed) != string([]byte{4, 5}) || scopes != "repo" {
		t.Fatalf("got %v %q %v", sealed, scopes, err)
	}
	if err := s.DeleteGitHubToken(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if sealed, _, _ := s.GitHubToken(ctx, u.ID); sealed != nil {
		t.Error("token must be deleted")
	}

	if err := s.SaveCluster(ctx, "prod", "h", u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRepoLink(ctx, fixpr.RepoLink{ClusterID: "prod", Repo: "o/r", Branch: "main", Path: "deploy"}); err != nil {
		t.Fatal(err)
	}
	l, err := s.GetRepoLink(ctx, "prod")
	if err != nil || l == nil || l.Repo != "o/r" || l.Path != "deploy" || l.UpdatedAt.IsZero() {
		t.Fatalf("link = %+v %v", l, err)
	}
	if err := s.DeleteCluster(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.GetRepoLink(ctx, "prod"); l != nil {
		t.Error("the link must cascade with the cluster")
	}
}
