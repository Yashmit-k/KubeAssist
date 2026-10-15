// Command agent runs INSIDE a customer's cluster with read-only RBAC and
// dials OUTBOUND to the KubeAssist relay (GET /agent/connect). It
// executes collection jobs the relay sends and returns redacted results.
// Detection, AI and storage stay central in the backend, so detector
// improvements never require redeploying agents.
//
// Required env: RELAY_URL (wss://host/agent/connect), CLUSTER_ID,
// AGENT_TOKEN.
package main

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/collector"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/k8s"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/redact"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/tunnel"
)

// version is stamped at build time via -ldflags.
var version = "dev"

const (
	jobTimeout = 30 * time.Second
	writeWait  = 10 * time.Second
	minBackoff = 2 * time.Second
	maxBackoff = 60 * time.Second
)

func main() {
	relayURL := requireEnv("RELAY_URL")
	clusterID := requireEnv("CLUSTER_ID")
	token := requireEnv("AGENT_TOKEN")

	if strings.HasPrefix(relayURL, "ws://") && !isLocalhost(relayURL) {
		log.Printf("agent: WARNING: RELAY_URL uses plain ws:// — the token travels unencrypted. Use wss:// in production.")
	}

	// Empty kubeconfig path => in-cluster ServiceAccount credentials.
	clientset, err := k8s.NewClientset(os.Getenv("KUBECONFIG"), os.Getenv("KUBE_CONTEXT"))
	if err != nil {
		log.Fatalf("agent: building kubernetes client: %v", err)
	}
	serverVersion := ""
	if v, err := clientset.Discovery().ServerVersion(); err == nil {
		serverVersion = v.GitVersion
	} else {
		log.Printf("agent: warning: could not fetch cluster server version: %v", err)
	}
	coll := collector.New(clientset, clusterID, serverVersion)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("agent %s starting for cluster %q (kubernetes %s)", version, clusterID, serverVersion)
	backoff := minBackoff
	for ctx.Err() == nil {
		connectedFor, err := runOnce(ctx, relayURL, clusterID, token, coll)
		if ctx.Err() != nil {
			break
		}
		// A connection that stayed up for a while resets the backoff; a
		// relay that keeps rejecting us backs off up to a minute.
		if connectedFor > time.Minute {
			backoff = minBackoff
		}
		wait := backoff + time.Duration(rand.Int64N(int64(backoff)/2))
		log.Printf("agent: relay connection ended (%v) — reconnecting in %s", err, wait.Round(time.Second))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
		backoff = min(backoff*2, maxBackoff)
	}
	log.Println("agent: shutting down")
}

// agentSession is one live connection to the relay.
type agentSession struct {
	conn    *websocket.Conn
	coll    *collector.Collector
	writeMu sync.Mutex // one concurrent writer per websocket
}

func runOnce(ctx context.Context, relayURL, clusterID, token string, coll *collector.Collector) (time.Duration, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	header.Set("X-Cluster-ID", clusterID)
	header.Set("User-Agent", "kubeassist-agent/"+version)

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, resp, err := websocket.DefaultDialer.DialContext(dialCtx, relayURL, header)
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return 0, errors.New("relay rejected the cluster ID or token (401) — check CLUSTER_ID/AGENT_TOKEN")
		}
		return 0, err
	}
	defer conn.Close()
	start := time.Now()
	log.Printf("agent: connected to relay as cluster %q", clusterID)

	// Close the socket on shutdown so ReadJSON below unblocks.
	go func() {
		<-ctx.Done()
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "agent shutting down"), time.Now().Add(time.Second))
		_ = conn.Close()
	}()

	// The relay pings every 30s; gorilla's default ping handler replies
	// with a pong. If nothing arrives for 90s, assume the relay is gone.
	const readWait = 90 * time.Second
	_ = conn.SetReadDeadline(time.Now().Add(readWait))
	conn.SetPingHandler(func(data string) error {
		_ = conn.SetReadDeadline(time.Now().Add(readWait))
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(writeWait))
	})

	s := &agentSession{conn: conn, coll: coll}
	for {
		var req tunnel.JobRequest
		if err := conn.ReadJSON(&req); err != nil {
			return time.Since(start), err
		}
		_ = conn.SetReadDeadline(time.Now().Add(readWait))
		go s.handleJob(req)
	}
}

func (s *agentSession) handleJob(req tunnel.JobRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	resp := tunnel.JobResponse{JobID: req.JobID}
	var err error
	switch req.Kind {
	case "", tunnel.JobSnapshot:
		resp.Snapshot, err = s.coll.CollectCore(ctx, req.Namespace, req.Pod)
		if err == nil {
			// Redact BEFORE anything leaves the customer's cluster.
			redact.Snapshot(resp.Snapshot)
		}
	case tunnel.JobRelated:
		resp.Related, err = s.coll.CollectRelated(ctx, req.Namespace, req.Pod, req.ServiceName)
	case tunnel.JobListNamespaces:
		resp.Namespaces, err = s.coll.ListNamespaces(ctx)
	case tunnel.JobListPods:
		resp.Pods, err = s.coll.ListPods(ctx, req.Namespace)
	case tunnel.JobLogs:
		var logs string
		logs, err = s.coll.FetchContainerLogs(ctx, req.Namespace, req.Pod, req.Container, req.Previous)
		if err == nil {
			logs = redact.Text(logs)
			resp.Logs = &logs
		}
	default:
		resp.Error = "unsupported job kind " + string(req.Kind) + " (agent " + version + ")"
	}
	if err != nil {
		resp.Error = err.Error()
		resp.NotFound = apierrors.IsNotFound(err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := s.conn.WriteJSON(resp); err != nil {
		log.Printf("agent: failed to send job %s result: %v", req.JobID, err)
	}
}

func isLocalhost(u string) bool {
	return strings.Contains(u, "://localhost") || strings.Contains(u, "://127.0.0.1") || strings.Contains(u, "host.docker.internal")
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("agent: required environment variable %s is not set", key)
	}
	return v
}
