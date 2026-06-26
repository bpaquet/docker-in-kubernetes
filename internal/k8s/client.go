// Package k8s wraps client-go for the daemon.
package k8s

import (
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Mode is local (via kubeconfig) or in-cluster (via service account).
type Mode int

// Mode values returned by Connect.
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

// ClientConfig configures Connect. Empty fields fall back to client-go defaults.
type ClientConfig struct {
	KubeconfigPath string
	Context        string
}

// Connection is the result of Connect.
type Connection struct {
	Clientset kubernetes.Interface
	REST      *rest.Config
	Mode      Mode
}

// Connect picks in-cluster vs kubeconfig auth automatically.
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
