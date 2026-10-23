package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/store"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/tunnel"
)

type registerClusterRequest struct {
	ClusterID string `json:"clusterId"`
}

// registerClusterResponse has everything the UI needs to render a
// ready-to-apply agent manifest. The token appears here exactly once.
type registerClusterResponse struct {
	ClusterID string `json:"clusterId"`
	Token     string `json:"token"`
	RelayURL  string `json:"relayUrl"`
	Manifest  string `json:"manifest"`
}

// handleRegisterCluster powers "Connect your cluster": a signed-in user
// picks a cluster ID and receives an agent manifest embedding a fresh
// agent token. The cluster belongs to that user. The token is only for
// the agent and is never retrievable again; only its hash is stored.
// AdminToken may also register (the cluster then has no owner and is
// operator-only).
func (s *Server) handleRegisterCluster(w http.ResponseWriter, r *http.Request) {
	ownerID := ""
	if !s.isAdmin(r) {
		u, ok := s.requireUser(w, r)
		if !ok {
			return
		}
		if ok, wait := s.RegisterLimiter.Allow("user:" + u.ID); !ok {
			writeRateLimited(w, wait)
			return
		}
		ownerID = u.ID
	}

	var req registerClusterRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not parse request body")
		return
	}
	req.ClusterID = strings.TrimSpace(req.ClusterID)
	if req.ClusterID == "" {
		writeError(w, http.StatusBadRequest, "missing_cluster_id", "clusterId is required")
		return
	}
	if !tunnel.ValidClusterID(req.ClusterID) {
		writeError(w, http.StatusBadRequest, "invalid_cluster_id", "clusterId must be 1-63 lowercase letters, digits or '-', starting and ending with a letter or digit")
		return
	}
	if req.ClusterID == store.LocalCluster {
		writeError(w, http.StatusBadRequest, "invalid_cluster_id", "clusterId \"local\" is reserved")
		return
	}

	token, err := s.Hub.RegisterCluster(req.ClusterID, ownerID, s.MaxClustersPerUser)
	if err != nil {
		switch {
		case errors.Is(err, tunnel.ErrAlreadyRegistered):
			writeError(w, http.StatusConflict, "cluster_already_registered", "that cluster ID is taken; choose another")
			return
		case errors.Is(err, tunnel.ErrTooManyClusters):
			writeError(w, http.StatusForbidden, "cluster_limit_reached", fmt.Sprintf("an account can register at most %d clusters; revoke one first", s.MaxClustersPerUser))
			return
		}
		writeError(w, http.StatusInternalServerError, "registration_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, registerClusterResponse{
		ClusterID: req.ClusterID,
		Token:     token,
		RelayURL:  s.RelayPublicURL,
		Manifest:  agentManifest(req.ClusterID, token, s.RelayPublicURL, s.AgentImage),
	})
}

// handleListClusters lists the caller's own clusters. With AdminToken
// it lists every registration (operator view).
func (s *Server) handleListClusters(w http.ResponseWriter, r *http.Request) {
	if s.isAdmin(r) {
		writeJSON(w, http.StatusOK, s.Hub.ListClusters())
		return
	}
	u, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.Hub.ListClustersOwnedBy(u.ID))
}

func (s *Server) handleRemoteStatus(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	writeJSON(w, http.StatusOK, s.Hub.Status(clusterID))
}

// handleRevokeCluster deletes a registration (and its history),
// disconnecting the agent. Authorized by ownership (see Server.remote).
func (s *Server) handleRevokeCluster(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	if err := s.Hub.RevokeCluster(clusterID); err != nil {
		writeError(w, http.StatusInternalServerError, "revoke_failed", err.Error())
		return
	}
	if s.RepoLinks != nil {
		if err := s.RepoLinks.DeleteRepoLink(r.Context(), clusterID); err != nil {
			log.Printf("api: deleting repo link of revoked cluster %q: %v", clusterID, err)
		}
	}
	if s.Alerts != nil {
		if err := s.Alerts.Store().DeleteAlertConfig(r.Context(), clusterID); err != nil {
			log.Printf("api: deleting alert config of revoked cluster %q: %v", clusterID, err)
		}
		s.Alerts.Reset(clusterID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// agentManifest renders a complete, read-only agent install manifest
// for one cluster: namespace, ServiceAccount, ClusterRole (get/list/watch
// only, no Secrets), binding, token Secret and Deployment.
//
// The pod template carries a short hash of the token. The agent reads
// the token from the Secret only at startup, so without this, applying
// a manifest with a NEW token (after re-registering) would update the
// Secret but leave the running pod on the old token, rejected by the
// relay. A changed annotation makes Kubernetes roll the pod.
func agentManifest(clusterID, token, relayURL, image string) string {
	if image == "" {
		image = "ghcr.io/yashmit-k/kubeassist-agent:latest"
	}
	tokenHash := tunnel.HashToken(token)[:16]
	return fmt.Sprintf(`# KubeAssist agent for cluster %[1]q
# Read-only: the agent can get/list/watch pods, events, logs, services,
# endpointslices, configmaps and workloads. It cannot read Secrets and
# cannot change anything. It connects OUTBOUND to the relay; nothing in
# your cluster is exposed.
apiVersion: v1
kind: Namespace
metadata:
  name: kubeassist
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kubeassist-agent
  namespace: kubeassist
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubeassist-agent-readonly
rules:
  - apiGroups: [""]
    resources: ["pods", "pods/log", "events", "services", "configmaps", "namespaces", "nodes"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["replicasets", "deployments", "statefulsets", "daemonsets"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kubeassist-agent-readonly
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kubeassist-agent-readonly
subjects:
  - kind: ServiceAccount
    name: kubeassist-agent
    namespace: kubeassist
---
apiVersion: v1
kind: Secret
metadata:
  name: kubeassist-agent-token
  namespace: kubeassist
type: Opaque
stringData:
  AGENT_TOKEN: %[2]q
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kubeassist-agent
  namespace: kubeassist
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kubeassist-agent
  template:
    metadata:
      labels:
        app: kubeassist-agent
      annotations:
        kubeassist.io/token-hash: %[5]q
    spec:
      serviceAccountName: kubeassist-agent
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: agent
          image: %[4]s
          env:
            - name: RELAY_URL
              value: %[3]q
            - name: CLUSTER_ID
              value: %[1]q
            - name: AGENT_TOKEN
              valueFrom:
                secretKeyRef:
                  name: kubeassist-agent-token
                  key: AGENT_TOKEN
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              memory: 128Mi
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
`, clusterID, token, relayURL, image, tokenHash)
}
