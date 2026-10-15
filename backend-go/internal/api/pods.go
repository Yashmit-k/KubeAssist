package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/detect"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/redact"
)

const listTimeout = 20 * time.Second

func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request, src clusterSource, _ string) {
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	namespaces, err := src.ListNamespaces(ctx)
	if err != nil {
		writeSourceError(w, err, "not_found")
		return
	}
	writeJSON(w, http.StatusOK, namespaces)
}

func (s *Server) handlePodList(w http.ResponseWriter, r *http.Request, src clusterSource, _ string) {
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	pods, err := src.ListPods(ctx, r.URL.Query().Get("namespace"))
	if err != nil {
		writeSourceError(w, err, "namespace_not_found")
		return
	}
	writeJSON(w, http.StatusOK, pods)
}

// handlePodDetail returns the pass-1 snapshot plus the detector's
// classification — the Pod Detail page renders containers, events and
// logs from this without invoking the AI service. The snapshot is
// redacted exactly as it would be before going to the AI.
func (s *Server) handlePodDetail(w http.ResponseWriter, r *http.Request, src clusterSource, _ string) {
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	snapshot, err := src.CollectCore(ctx, r.PathValue("ns"), r.PathValue("name"))
	if err != nil {
		writeSourceError(w, err, "pod_not_found")
		return
	}
	snapshot.Detector = detect.Detect(snapshot)
	redact.Snapshot(snapshot)
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handlePodLogs(w http.ResponseWriter, r *http.Request, src clusterSource, _ string) {
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	q := r.URL.Query()
	logs, err := src.FetchContainerLogs(ctx, r.PathValue("ns"), r.PathValue("name"), q.Get("container"), q.Get("previous") == "true")
	if err != nil {
		// "previous terminated container not found" is the API's normal
		// answer when a container never restarted — surface it plainly.
		if strings.Contains(err.Error(), "previous terminated container") {
			writeJSON(w, http.StatusOK, map[string]string{"logs": "", "unavailableReason": "no previous container instance (it has not restarted)"})
			return
		}
		writeSourceError(w, err, "pod_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}
