package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

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
	if _, err := s.db.ExecContext(ctx, `TRUNCATE investigations; TRUNCATE clusters;`); err != nil {
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
	if err := s.SaveCluster(ctx, "prod", "hash-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCluster(ctx, "prod", "hash-2"); err == nil {
		t.Error("duplicate cluster id must fail")
	}
	snap := model.DiagnosticSnapshot{SchemaVersion: "1.1.0"}
	if _, err := s.Save(ctx, "prod", "ns", "p", snap, diagnosis("Healthy")); err != nil {
		t.Fatal(err)
	}

	all, err := s.LoadClusters(ctx)
	if err != nil || all["prod"] != "hash-1" {
		t.Fatalf("LoadClusters = %v, %v", all, err)
	}
	if err := s.DeleteCluster(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	all, _ = s.LoadClusters(ctx)
	list, _ := s.List(ctx, "prod", "", 10)
	if len(all) != 0 || len(list) != 0 {
		t.Errorf("delete should remove registration and history: %v %v", all, list)
	}
}
