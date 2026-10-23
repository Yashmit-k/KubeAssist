package fixpr

import (
	"context"
	"sync"
	"time"
)

// RepoLink ties a cluster to the repository folder holding its
// manifests. Fix PRs are opened against Branch.
type RepoLink struct {
	ClusterID string    `json:"clusterId"`
	Repo      string    `json:"repo"`   // "owner/name"
	Branch    string    `json:"branch"` // base branch for PRs
	Path      string    `json:"path"`   // folder searched for manifests ("" = whole repo)
	UpdatedAt time.Time `json:"updatedAt"`
}

// LinkStore persists repo links (Postgres in internal/store).
type LinkStore interface {
	GetRepoLink(ctx context.Context, clusterID string) (*RepoLink, error) // nil, nil when none
	PutRepoLink(ctx context.Context, l RepoLink) error
	DeleteRepoLink(ctx context.Context, clusterID string) error
}

// MemoryLinks is the no-database LinkStore.
type MemoryLinks struct {
	mu    sync.Mutex
	links map[string]RepoLink
}

func NewMemoryLinks() *MemoryLinks { return &MemoryLinks{links: map[string]RepoLink{}} }

func (m *MemoryLinks) GetRepoLink(_ context.Context, id string) (*RepoLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[id]
	if !ok {
		return nil, nil
	}
	return &l, nil
}

func (m *MemoryLinks) PutRepoLink(_ context.Context, l RepoLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l.UpdatedAt = time.Now().UTC()
	m.links[l.ClusterID] = l
	return nil
}

func (m *MemoryLinks) DeleteRepoLink(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.links, id)
	return nil
}
