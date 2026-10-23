// Package alerts notifies cluster owners when a problem starts or ends:
// a background watcher runs the health scan on every cluster that has
// alerts enabled, compares root causes with the previous scans, and
// posts new and resolved problems to the owner's webhook (Slack,
// Discord, or generic JSON).
//
// It reuses the scan rather than streaming events from agents, so it
// works with every agent version and costs one pod list per interval on
// a healthy cluster.
package alerts

import (
	"context"
	"sync"
	"time"
)

// Webhook kinds.
const (
	KindSlack   = "slack"
	KindDiscord = "discord"
	KindWebhook = "webhook" // generic JSON
)

// ValidKind reports whether k is a supported webhook kind.
func ValidKind(k string) bool { return k == KindSlack || k == KindDiscord || k == KindWebhook }

// Config is one cluster's alert destination. URL is a secret (Slack and
// Discord webhook URLs grant posting rights) and is never sent back to
// the browser unmasked.
type Config struct {
	ClusterID string
	OwnerID   string
	Kind      string
	URL       string
	Enabled   bool
	UpdatedAt time.Time
}

// Store persists alert configs. internal/store implements it on
// Postgres; MemoryStore is the no-database fallback.
type Store interface {
	GetAlertConfig(ctx context.Context, clusterID string) (*Config, error) // nil, nil when none
	PutAlertConfig(ctx context.Context, c Config) error
	DeleteAlertConfig(ctx context.Context, clusterID string) error
	ListAlertConfigs(ctx context.Context) ([]Config, error)
}

// Cause is one root cause from a scan, as alerts need it.
type Cause struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	FailureType string   `json:"failureType"`
	Pods        []string `json:"pods"` // "namespace/name"
	Fix         string   `json:"fix,omitempty"`
}

// MemoryStore keeps configs in process memory (lost on restart).
type MemoryStore struct {
	mu      sync.Mutex
	configs map[string]Config
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{configs: map[string]Config{}} }

func (m *MemoryStore) GetAlertConfig(_ context.Context, clusterID string) (*Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.configs[clusterID]
	if !ok {
		return nil, nil
	}
	return &c, nil
}

func (m *MemoryStore) PutAlertConfig(_ context.Context, c Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.UpdatedAt = time.Now().UTC()
	m.configs[c.ClusterID] = c
	return nil
}

func (m *MemoryStore) DeleteAlertConfig(_ context.Context, clusterID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.configs, clusterID)
	return nil
}

func (m *MemoryStore) ListAlertConfigs(context.Context) ([]Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Config, 0, len(m.configs))
	for _, c := range m.configs {
		out = append(out, c)
	}
	return out, nil
}
