// Command agent is a small binary meant to run AS A POD INSIDE a
// customer's Kubernetes cluster. It authenticates to that cluster using
// IN-CLUSTER config (its own ServiceAccount, same read-only RBAC pattern
// as k8s/rbac/rbac.yaml), connects OUTBOUND to the central relay, and
// answers JobRequests by running the exact same collector.CollectCore
// logic the main backend uses locally.
//
// This is deliberately a THIN wrapper — all the real work (talking to
// Kubernetes, building a snapshot) is internal/collector, unchanged. The
// only new code here is the outbound connection and message loop.
package main

import (
	"context"
	"log"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/yourname/kubeassist/backend-go/internal/collector"
	"github.com/yourname/kubeassist/backend-go/internal/k8s"
	"github.com/yourname/kubeassist/backend-go/internal/redact"
	"github.com/yourname/kubeassist/backend-go/internal/tunnel"
)

func main() {
	relayURL := requireEnv("RELAY_URL") // e.g. wss://kubeassist.example.com/agent/connect
	clusterID := requireEnv("CLUSTER_ID")
	token := requireEnv("AGENT_TOKEN")

	// Empty kubeconfig path => internal/k8s falls through to
	// rest.InClusterConfig(), reading the ServiceAccount token
	// Kubernetes automatically mounts into this Pod. This is the EXACT
	// same k8s.NewClientset function main.go uses for the primary
	// backend — no new client construction code needed.
	clientset, err := k8s.NewClientset("", "")
	if err != nil {
		log.Fatalf("agent: building in-cluster client: %v", err)
	}

	serverVersion := ""
	if v, err := clientset.Discovery().ServerVersion(); err == nil {
		serverVersion = v.GitVersion
	} else {
		log.Printf("agent: warning: could not fetch cluster server version: %v", err)
	}

	coll := collector.New(clientset, clusterID, serverVersion)

	for {
		if err := runOnce(relayURL, clusterID, token, coll); err != nil {
			log.Printf("agent: connection to relay lost: %v — reconnecting in 5s", err)
		}
		time.Sleep(5 * time.Second)
	}
}

// runOnce establishes one connection to the relay and services jobs
// until that connection drops, then returns (letting main's loop
// reconnect). Reconnect-with-backoff is essential here: this agent
// might start before the relay is reachable, or the relay might restart
// during a deploy — the agent should keep trying rather than exiting.
func runOnce(relayURL, clusterID, token string, coll *collector.Collector) error {
	u, err := url.Parse(relayURL)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("clusterId", clusterID)
	q.Set("token", token)
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("agent: connected to relay as cluster %q", clusterID)

	for {
		var req tunnel.JobRequest
		if err := conn.ReadJSON(&req); err != nil {
			return err // connection dropped — caller will reconnect
		}

		// Each job runs in its own goroutine so a slow snapshot (many
		// containers, large logs) doesn't block this agent from
		// starting the NEXT job that arrives while the first is still
		// in flight — same reasoning as why the main backend's
		// collector fetches Pod/Events/Owner/Logs concurrently.
		go handleJob(conn, coll, req)
	}
}

func handleJob(conn *websocket.Conn, coll *collector.Collector, req tunnel.JobRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp := tunnel.JobResponse{JobID: req.JobID}

	snapshot, err := coll.CollectCore(ctx, req.Namespace, req.Pod)
	if err != nil {
		resp.Error = err.Error()
	} else {
		// Redact BEFORE it ever leaves this cluster's network boundary
		// — the same trust-boundary principle as the local flow, just
		// enforced one hop earlier, since this snapshot is about to
		// cross into the relay's infrastructure over the WebSocket.
		redact.Snapshot(snapshot)
		resp.Snapshot = snapshot
	}

	// Same writeMu concern as the hub side: if multiple job goroutines
	// finish around the same time, they must not call WriteJSON
	// concurrently on the same *websocket.Conn. A production version
	// would share one writer goroutine fed by a channel; for now, a
	// package-level mutex per connection keeps this correct with
	// minimal code — acceptable since job volume per agent is low
	// relative to, say, a high-throughput API.
	writeMu.Lock()
	_ = conn.WriteJSON(resp)
	writeMu.Unlock()
}

var writeMu sync.Mutex

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("agent: required environment variable %s is not set", key)
	}
	return v
}
