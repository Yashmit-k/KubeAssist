// Package k8s owns the ONE thing in this codebase allowed to construct an
// authenticated Kubernetes API client. Per the plan's trust boundary
// ("Only Go holds kubeconfig"), this is deliberately a small, tightly
// scoped package — collector, detect, redact etc. all receive an already
// -built clientset, they never touch kubeconfig or credentials directly.
package k8s

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewClientset builds an authenticated Kubernetes clientset.
//
//   - kubeconfigPath: filesystem path to a kubeconfig file. Pass "" to
//     attempt in-cluster config instead (see below).
//   - kubeContext: optional context name to select within that kubeconfig
//     (e.g. "kind-kubeassist-dev"). Pass "" to use the kubeconfig's
//     current-context as-is.
//
// WHY RETURN *kubernetes.Clientset, error (not panic on failure)?
// A failed cluster connection is an entirely ordinary, expected runtime
// condition (wrong context, cluster down, expired token, VPN not
// connected) — not a programming bug. Go reserves panics for programmer
// errors and truly unrecoverable states (like array index out of
// bounds), NOT for expected failure modes of external systems. Every
// external-system call in this codebase (Kubernetes, the AI service,
// Postgres) will follow this same (result, error) shape.
const (
	clientQPS   = 50
	clientBurst = 100
)

func NewClientset(kubeconfigPath, kubeContext string) (*kubernetes.Clientset, error) {
	restConfig, err := buildRESTConfig(kubeconfigPath, kubeContext)
	if err != nil {
		return nil, fmt.Errorf("k8s: building rest config: %w", err)
	}
	// client-go's default client-side limit (5 QPS, burst 10) throttles
	// a health scan, which collects several pods in parallel at ~6 reads
	// each. These are all reads; 50 QPS is still gentle on an API server.
	restConfig.QPS = clientQPS
	restConfig.Burst = clientBurst

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("k8s: creating clientset: %w", err)
	}

	return clientset, nil
}

// buildRESTConfig resolves a *rest.Config — client-go's internal
// representation of "everything needed to make an authenticated HTTP
// request to an API server": the server URL, TLS/CA info, and auth
// credentials (bearer token, client cert, or exec-plugin callback for
// cloud providers like EKS/GKE).
func buildRESTConfig(kubeconfigPath, kubeContext string) (*rest.Config, error) {
	if kubeconfigPath == "" {
		// No kubeconfig path given — assume we're running INSIDE a
		// Kubernetes Pod and should use the automatically-mounted
		// ServiceAccount token. rest.InClusterConfig() looks for the
		// standard mounted paths and env vars (KUBERNETES_SERVICE_HOST /
		// KUBERNETES_SERVICE_PORT, which the kubelet injects into every
		// Pod automatically) and fails with a clear error if they're
		// absent — which correctly happens when you run this binary
		// directly on your Mac without KUBECONFIG set.
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("no kubeconfig path given and in-cluster config unavailable "+
				"(are you running outside a cluster without KUBECONFIG set?): %w", err)
		}
		return cfg, nil
	}

	// clientcmd.NewNonInteractiveDeferredLoadingClientConfig is the same
	// loading logic kubectl itself uses: it parses the kubeconfig YAML,
	// resolves the requested context (or falls back to
	// current-context), and produces something that can be turned into
	// a *rest.Config. We use the "overrides" parameter to force a
	// specific context when kubeContext is non-empty — this is exactly
	// how `kubectl --context=X` works under the hood.
	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
	overrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		overrides.CurrentContext = kubeContext
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig from %q (context %q): %w",
			kubeconfigPath, kubeContext, err)
	}

	return restConfig, nil
}
