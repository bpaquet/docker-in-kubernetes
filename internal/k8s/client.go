// Package k8s wraps client-go for the daemon: connecting, pod CRUD, watching.
package k8s

import (
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Mode describes whether the daemon is running inside the cluster (auto-
// detected via KUBERNETES_SERVICE_HOST) or against an external cluster via
// kubeconfig.
type Mode int

// Mode constants returned by Connect.
const (
	ModeLocal Mode = iota
	ModeInCluster
)

func (m Mode) String() string {
	if m == ModeInCluster {
		return "in-cluster"
	}
	return "local"
}

// ClientConfig configures Connect.
type ClientConfig struct {
	// KubeconfigPath is consulted only in ModeLocal. Empty means: try KUBECONFIG
	// env, then ~/.kube/config (client-go default precedence).
	KubeconfigPath string
	// Context overrides the kubeconfig current-context. Empty = use current.
	Context string
}

// Connection bundles the products of Connect.
type Connection struct {
	Clientset kubernetes.Interface
	REST      *rest.Config
	Mode      Mode
}

// Connect builds a kubernetes.Interface and reports which mode was used.
func Connect(cfg ClientConfig) (*Connection, error) {
	if inCluster() {
		restCfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
		cs, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return nil, fmt.Errorf("kubernetes clientset: %w", err)
		}
		return &Connection{Clientset: cs, REST: restCfg, Mode: ModeInCluster}, nil
	}

	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	if cfg.KubeconfigPath != "" {
		loading.ExplicitPath = cfg.KubeconfigPath
	}
	overrides := &clientcmd.ConfigOverrides{}
	if cfg.Context != "" {
		overrides.CurrentContext = cfg.Context
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes clientset: %w", err)
	}
	return &Connection{Clientset: cs, REST: restCfg, Mode: ModeLocal}, nil
}

func inCluster() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}
