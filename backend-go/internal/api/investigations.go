package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/store"
)

// Investigation history is always scoped to one cluster: the local
// cluster's history is only reachable in local mode, and a remote
// cluster's only with that cluster's token. That closes the gap where
// any caller could list every customer's saved diagnoses.

func (s *Server) handleListInvestigations(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterKey string) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "investigation history is not configured (no DATABASE_URL)")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.Store.List(r.Context(), clusterKey, r.URL.Query().Get("namespace"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetInvestigation(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterKey string) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "investigation history is not configured (no DATABASE_URL)")
		return
	}
	inv, err := s.Store.Get(r.Context(), clusterKey, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "investigation_not_found", "no investigation with that id for this cluster")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inv)
}
