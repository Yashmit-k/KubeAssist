package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
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

// handleRegisterCluster powers self-serve "Connect your cluster": the
// caller picks a cluster ID and receives a fresh token plus an agent
// manifest. The token is never retrievable again (GitHub-PAT style);
// only its hash is stored.
func (s *Server) handleRegisterCluster(w http.ResponseWriter, r *http.Request) {
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

	token, err := s.Hub.RegisterCluster(req.ClusterID)
	if err != nil {
		if strings.Contains(err.Error(), "already registered") {
			writeError(w, http.StatusConflict, "cluster_already_registered", err.Error())
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

// handleListClusters lists every registered cluster. It's an operator
// view: in a public deployment (local mode off) it requires ADMIN_TOKEN,
// so customers can't enumerate each other's cluster IDs.
func (s *Server) handleListClusters(w http.ResponseWriter, r *http.Request) {
	switch {
	case s.AdminToken != "":
		if subtle.ConstantTimeCompare([]byte(bearerToken(r)), []byte(s.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "admin_token_required", "listing all clusters requires the admin token")
			return
		}
	case !s.LocalModeEnabled:
		writeError(w, http.StatusForbidden, "listing_disabled", "listing all clusters is disabled in multi-tenant mode (set ADMIN_TOKEN to enable it for operators)")
		return
	}
	writeJSON(w, http.StatusOK, s.Hub.ListClusters())
}

func (s *Server) handleRemoteStatus(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	writeJSON(w, http.StatusOK, s.Hub.Status(clusterID))
}

// handleRevokeCluster deletes a registration (and its history),
// disconnecting the agent. Authorized by the cluster's own token.
func (s *Server) handleRevokeCluster(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	if err := s.Hub.RevokeCluster(clusterID); err != nil {
		writeError(w, http.StatusInternalServerError, "revoke_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// agentManifest renders a complete, read-only agent install manifest
// for one cluster: namespace, ServiceAccount, ClusterRole (get/list/watch
// only, no Secrets), binding, token Secret and Deployment.
func agentManifest(clusterID, token, relayURL, image string) string {
	if image == "" {
		image = "ghcr.io/yashmit-k/kubeassist-agent:latest"
	}
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
`, clusterID, token, relayURL, image)
}
