package api

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/collector"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/redact"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/tunnel"
)

// clusterSource is everything the handlers need from "a cluster",
// whether it is this backend's own cluster (reached directly through
// client-go) or a customer's cluster (reached through the agent tunnel).
// Every handler is written once against this interface, so the remote
// path gets exactly the same two-pass collection, streaming and history
// as the local one.
type clusterSource interface {
	CollectCore(ctx context.Context, namespace, pod string) (*model.DiagnosticSnapshot, error)
	CollectRelated(ctx context.Context, namespace, pod string, serviceName *string) (*model.RelatedInfo, error)
	ListNamespaces(ctx context.Context) ([]string, error)
	ListPods(ctx context.Context, namespace string) ([]model.PodSummary, error)
	FetchContainerLogs(ctx context.Context, namespace, pod, container string, previous bool) (string, error)
}

var _ clusterSource = (*collector.Collector)(nil)

// remoteSource adapts the tunnel Hub to clusterSource for one cluster.
type remoteSource struct {
	hub       *tunnel.Hub
	clusterID string
}

func (r remoteSource) CollectCore(ctx context.Context, namespace, pod string) (*model.DiagnosticSnapshot, error) {
	return r.hub.Dispatch(ctx, r.clusterID, tunnel.JobRequest{Namespace: namespace, Pod: pod})
}

func (r remoteSource) CollectRelated(ctx context.Context, namespace, pod string, serviceName *string) (*model.RelatedInfo, error) {
	resp, err := r.hub.Do(ctx, r.clusterID, tunnel.JobRequest{Kind: tunnel.JobRelated, Namespace: namespace, Pod: pod, ServiceName: serviceName})
	if err != nil {
		return nil, err
	}
	if resp.Related == nil {
		// Agents built before job kinds existed answer every job with a
		// snapshot; treat that as "no related info" rather than an error.
		return nil, fmt.Errorf("agent does not support related-info collection (upgrade the agent)")
	}
	return resp.Related, nil
}

func (r remoteSource) ListNamespaces(ctx context.Context) ([]string, error) {
	resp, err := r.hub.Do(ctx, r.clusterID, tunnel.JobRequest{Kind: tunnel.JobListNamespaces})
	if err != nil {
		return nil, err
	}
	if resp.Namespaces == nil {
		return []string{}, nil
	}
	return resp.Namespaces, nil
}

func (r remoteSource) ListPods(ctx context.Context, namespace string) ([]model.PodSummary, error) {
	resp, err := r.hub.Do(ctx, r.clusterID, tunnel.JobRequest{Kind: tunnel.JobListPods, Namespace: namespace})
	if err != nil {
		return nil, err
	}
	if resp.Pods == nil {
		return []model.PodSummary{}, nil
	}
	return resp.Pods, nil
}

func (r remoteSource) FetchContainerLogs(ctx context.Context, namespace, pod, container string, previous bool) (string, error) {
	resp, err := r.hub.Do(ctx, r.clusterID, tunnel.JobRequest{Kind: tunnel.JobLogs, Namespace: namespace, Pod: pod, Container: container, Previous: previous})
	if err != nil {
		return "", err
	}
	if resp.Logs == nil {
		return "", nil
	}
	return *resp.Logs, nil
}

// redactingSource wraps the local collector so raw log text served by
// the standalone logs endpoint is scrubbed the same way snapshots are.
type redactingSource struct{ clusterSource }

func (r redactingSource) FetchContainerLogs(ctx context.Context, namespace, pod, container string, previous bool) (string, error) {
	logs, err := r.clusterSource.FetchContainerLogs(ctx, namespace, pod, container, previous)
	return redact.Text(logs), err
}

// isNotFound reports whether err means "that object doesn't exist",
// for either source type.
func isNotFound(err error) bool {
	return apierrors.IsNotFound(err) || errors.Is(err, tunnel.ErrNotFound)
}
