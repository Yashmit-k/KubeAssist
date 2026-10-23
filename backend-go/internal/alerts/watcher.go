package alerts

import (
	"context"
	"errors"
	"log"
	"sort"
	"sync"
	"time"
)

// confirmScans is how many consecutive scans must agree before a problem
// counts as started or resolved. Crash-looping pods can look fine between
// restarts; requiring agreement keeps alerts from flapping.
const confirmScans = 2

// ScanFunc runs a health scan of one cluster and returns its root causes.
// It returns ErrUnavailable when the cluster can't be scanned right now
// (e.g. its agent is offline), which is not reported as an error.
type ScanFunc func(ctx context.Context, clusterID string) ([]Cause, error)

// ErrUnavailable means "skip this cluster this round".
var ErrUnavailable = errors.New("cluster unavailable")

// ActiveCause is a problem the watcher currently considers ongoing.
type ActiveCause struct {
	Cause
	Since time.Time `json:"since"`
}

// Status is what the UI shows about a cluster's alerting.
type Status struct {
	LastScanAt        *time.Time    `json:"lastScanAt,omitempty"`
	LastScanError     string        `json:"lastScanError,omitempty"`
	Baseline          bool          `json:"baseline"` // first scan done; problems found then were not alerted
	Active            []ActiveCause `json:"active"`
	LastNotifiedAt    *time.Time    `json:"lastNotifiedAt,omitempty"`
	LastDeliveryError string        `json:"lastDeliveryError,omitempty"`
}

type tracked struct {
	cause  Cause
	since  time.Time
	misses int // consecutive scans without it
}

type clusterState struct {
	baselined bool
	pending   map[string]int // key -> consecutive sightings, not yet active
	active    map[string]*tracked
	status    Status
}

// Watcher periodically scans clusters with enabled alert configs.
type Watcher struct {
	store    Store
	scan     ScanFunc
	sender   *Sender
	interval time.Duration
	link     func(clusterID string) string

	mu     sync.Mutex
	states map[string]*clusterState
}

// NewWatcher builds a watcher; link returns the URL a notification
// should point to for a cluster (may return "").
func NewWatcher(store Store, scan ScanFunc, sender *Sender, interval time.Duration, link func(string) string) *Watcher {
	return &Watcher{store: store, scan: scan, sender: sender, interval: interval, link: link, states: map[string]*clusterState{}}
}

func (w *Watcher) Store() Store    { return w.store }
func (w *Watcher) Sender() *Sender { return w.sender }
func (w *Watcher) Link(id string) string {
	if w.link == nil {
		return ""
	}
	return w.link(id)
}

// SetScanner sets the scan function (the API server provides it and is
// built after the watcher).
func (w *Watcher) SetScanner(scan ScanFunc) { w.scan = scan }

// Run checks every interval until ctx ends. The first round waits one
// interval: right after a restart agents are still reconnecting, and an
// immediate check would only report them as offline.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		w.CheckAll(ctx)
	}
}

// CheckAll runs one round over every enabled config.
func (w *Watcher) CheckAll(ctx context.Context) {
	configs, err := w.store.ListAlertConfigs(ctx)
	if err != nil {
		log.Printf("alerts: listing configs: %v", err)
		return
	}
	for _, c := range configs {
		if !c.Enabled {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, w.interval)
		w.Check(cctx, c)
		cancel()
	}
}

// Check scans one cluster, updates its state and sends a notification
// if problems started or were resolved.
func (w *Watcher) Check(ctx context.Context, c Config) {
	causes, err := w.scan(ctx, c.ClusterID)
	now := time.Now().UTC()

	w.mu.Lock()
	st := w.state(c.ClusterID)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			st.status.LastScanError = "agent not connected"
		} else {
			st.status.LastScanError = err.Error()
		}
		w.mu.Unlock()
		return
	}
	st.status.LastScanAt, st.status.LastScanError = &now, ""
	current := make(map[string]Cause, len(causes))
	for _, cause := range causes {
		current[cause.Key] = cause
	}

	var started, resolved []Cause
	if !st.baselined {
		// First scan: whatever is broken now is the baseline. Alerting
		// on all of it would be noise; the UI lists it as tracked.
		for k, cause := range current {
			st.active[k] = &tracked{cause: cause, since: now}
		}
		st.baselined, st.status.Baseline = true, true
	} else {
		for k, cause := range current {
			if t, ok := st.active[k]; ok {
				t.cause, t.misses = cause, 0
				continue
			}
			st.pending[k]++
			if st.pending[k] >= confirmScans {
				delete(st.pending, k)
				st.active[k] = &tracked{cause: cause, since: now}
				started = append(started, cause)
			}
		}
		for k := range st.pending {
			if _, ok := current[k]; !ok {
				delete(st.pending, k) // seen once, gone again: never alerted
			}
		}
		for k, t := range st.active {
			if _, ok := current[k]; ok {
				continue
			}
			t.misses++
			if t.misses >= confirmScans {
				delete(st.active, k)
				resolved = append(resolved, t.cause)
			}
		}
	}
	st.status.Active = st.activeList()
	w.mu.Unlock()

	if len(started) == 0 && len(resolved) == 0 {
		return
	}
	sortCauses(started)
	sortCauses(resolved)
	err = w.sender.Send(ctx, c.Kind, c.URL, Notice{Cluster: c.ClusterID, New: started, Resolved: resolved, Link: w.Link(c.ClusterID)})

	w.mu.Lock()
	defer w.mu.Unlock()
	st = w.state(c.ClusterID)
	if err != nil {
		log.Printf("alerts: delivering for cluster %q: %v", c.ClusterID, err)
		st.status.LastDeliveryError = err.Error()
		return
	}
	t := time.Now().UTC()
	st.status.LastNotifiedAt, st.status.LastDeliveryError = &t, ""
}

// Status returns a copy of a cluster's alert status.
func (w *Watcher) Status(clusterID string) Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	st, ok := w.states[clusterID]
	if !ok {
		return Status{Active: []ActiveCause{}}
	}
	out := st.status
	out.Active = append([]ActiveCause{}, st.status.Active...)
	return out
}

// Reset forgets a cluster's state (config changed or deleted): the next
// scan becomes a fresh baseline.
func (w *Watcher) Reset(clusterID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.states, clusterID)
}

// RecordDelivery stores the outcome of a manual send (test message).
func (w *Watcher) RecordDelivery(clusterID string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.state(clusterID)
	if err != nil {
		st.status.LastDeliveryError = err.Error()
		return
	}
	t := time.Now().UTC()
	st.status.LastNotifiedAt, st.status.LastDeliveryError = &t, ""
}

func (w *Watcher) state(id string) *clusterState {
	st, ok := w.states[id]
	if !ok {
		st = &clusterState{pending: map[string]int{}, active: map[string]*tracked{}, status: Status{Active: []ActiveCause{}}}
		w.states[id] = st
	}
	return st
}

func (st *clusterState) activeList() []ActiveCause {
	out := make([]ActiveCause, 0, len(st.active))
	for _, t := range st.active {
		out = append(out, ActiveCause{Cause: t.cause, Since: t.since})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

func sortCauses(c []Cause) {
	sort.Slice(c, func(i, j int) bool { return c[i].Title < c[j].Title })
}
