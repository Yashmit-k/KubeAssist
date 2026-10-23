package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/alerts"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/tunnel"
)

// AlertScanner adapts the health scan for the alerts watcher: remote
// clusters only (alerts belong to an owner), skipped while the agent is
// offline.
func (s *Server) AlertScanner() alerts.ScanFunc {
	return func(ctx context.Context, clusterID string) ([]alerts.Cause, error) {
		if s.Hub == nil || !s.Hub.IsConnected(clusterID) {
			return nil, alerts.ErrUnavailable
		}
		res, err := runScan(ctx, remoteSource{hub: s.Hub, clusterID: clusterID}, "")
		if err != nil {
			if errors.Is(err, tunnel.ErrNotConnected) {
				return nil, alerts.ErrUnavailable
			}
			return nil, err
		}
		out := make([]alerts.Cause, 0, len(res.Groups))
		for _, g := range res.Groups {
			c := alerts.Cause{Key: g.Key, Title: g.Title, FailureType: g.FailureType}
			for _, p := range g.Pods {
				c.Pods = append(c.Pods, p.Namespace+"/"+p.Name)
			}
			if g.Fix != nil {
				for _, st := range g.Fix.Steps {
					if st.IsWriteAction && st.Command != "" {
						c.Fix = st.Command // the primary fix; a rollback comes last
						break
					}
				}
			}
			out = append(out, c)
		}
		return out, nil
	}
}

type alertConfigView struct {
	Configured bool          `json:"configured"`
	Kind       string        `json:"kind,omitempty"`
	URL        string        `json:"url,omitempty"` // masked
	Enabled    bool          `json:"enabled"`
	UpdatedAt  *time.Time    `json:"updatedAt,omitempty"`
	Status     alerts.Status `json:"status"`
	Interval   string        `json:"interval"`
}

type alertConfigRequest struct {
	Kind    string `json:"kind"`
	URL     string `json:"url,omitempty"` // empty keeps the stored URL
	Enabled bool   `json:"enabled"`
}

func (s *Server) handleGetAlerts(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	cfg, err := s.Alerts.Store().GetAlertConfig(r.Context(), clusterID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "alerts_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.alertView(clusterID, cfg))
}

func (s *Server) alertView(clusterID string, cfg *alerts.Config) alertConfigView {
	v := alertConfigView{Status: s.Alerts.Status(clusterID), Interval: s.AlertInterval.String()}
	if cfg != nil {
		t := cfg.UpdatedAt
		v.Configured, v.Kind, v.URL, v.Enabled, v.UpdatedAt = true, cfg.Kind, alerts.MaskURL(cfg.URL), cfg.Enabled, &t
	}
	return v
}

func (s *Server) handlePutAlerts(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	var req alertConfigRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not parse request body")
		return
	}
	req.Kind, req.URL = strings.TrimSpace(req.Kind), strings.TrimSpace(req.URL)
	if !alerts.ValidKind(req.Kind) {
		writeError(w, http.StatusBadRequest, "invalid_kind", "kind must be slack, discord or webhook")
		return
	}
	owner, _ := s.Hub.Owner(clusterID)
	if owner == "" {
		writeError(w, http.StatusBadRequest, "no_owner", "alerts need a cluster that belongs to an account")
		return
	}
	store := s.Alerts.Store()
	existing, err := store.GetAlertConfig(r.Context(), clusterID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "alerts_unavailable", err.Error())
		return
	}
	if req.URL == "" {
		if existing == nil {
			writeError(w, http.StatusBadRequest, "missing_url", "a webhook URL is required")
			return
		}
		req.URL = existing.URL // the UI only ever sees the masked form
	}
	if err := s.Alerts.Sender().ValidateURL(req.URL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_url", err.Error())
		return
	}
	cfg := alerts.Config{ClusterID: clusterID, OwnerID: owner, Kind: req.Kind, URL: req.URL, Enabled: req.Enabled}
	if err := store.PutAlertConfig(r.Context(), cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "alerts_unavailable", err.Error())
		return
	}
	// A new destination or re-enabling starts from a fresh baseline.
	if existing == nil || existing.URL != cfg.URL || !existing.Enabled {
		s.Alerts.Reset(clusterID)
	}
	saved, _ := store.GetAlertConfig(r.Context(), clusterID)
	writeJSON(w, http.StatusOK, s.alertView(clusterID, saved))
}

func (s *Server) handleDeleteAlerts(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	if err := s.Alerts.Store().DeleteAlertConfig(r.Context(), clusterID); err != nil {
		writeError(w, http.StatusInternalServerError, "alerts_unavailable", err.Error())
		return
	}
	s.Alerts.Reset(clusterID)
	w.WriteHeader(http.StatusNoContent)
}

// handleTestAlert sends a test message to the stored webhook.
func (s *Server) handleTestAlert(w http.ResponseWriter, r *http.Request, _ clusterSource, clusterID string) {
	cfg, err := s.Alerts.Store().GetAlertConfig(r.Context(), clusterID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "alerts_unavailable", err.Error())
		return
	}
	if cfg == nil {
		writeError(w, http.StatusNotFound, "alerts_not_configured", "save a webhook first")
		return
	}
	err = s.Alerts.Sender().Send(r.Context(), cfg.Kind, cfg.URL, alerts.Notice{Cluster: clusterID, Test: true, Link: s.Alerts.Link(clusterID)})
	s.Alerts.RecordDelivery(clusterID, err)
	if err != nil {
		log.Printf("alerts: test delivery for %q failed: %v", clusterID, err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
