// Package kube builds Kubernetes client configuration from either a kubeconfig
// file on the server or raw kubeconfig content (e.g. uploaded through the UI).
package kube

import (
	"errors"
	"fmt"
	"sort"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Context describes one context in a kubeconfig.
type Context struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
	Server    string `json:"server"`
	Namespace string `json:"namespace,omitempty"`
}

// InClusterPath is the kubeconfig_path value that selects strict in-cluster
// mode: only the pod's service account credentials are used.
const InClusterPath = "in-cluster"

// RestConfig resolves a *rest.Config. Precedence (path == InClusterPath is
// strict in-cluster mode and skips all of this):
//  1. data (raw kubeconfig content), if non-empty;
//  2. path, if non-empty;
//  3. in-cluster credentials, then the default loading rules (KUBECONFIG / ~/.kube/config).
//
// kubeContext selects a named context; empty means the kubeconfig's current-context.
func RestConfig(path string, data []byte, kubeContext string) (*rest.Config, error) {
	if path == InClusterPath {
		// Explicit in-cluster mode never falls back to a local kubeconfig:
		// on a workstation that could silently point KYMO at another
		// (e.g. production) cluster.
		cfg, err := rest.InClusterConfig()
		if errors.Is(err, rest.ErrNotInCluster) {
			return nil, fmt.Errorf("in-cluster mode: KYMO is not running inside Kubernetes (no service account credentials)")
		}
		return cfg, err
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}

	if len(data) > 0 {
		raw, err := clientcmd.Load(data)
		if err != nil {
			return nil, fmt.Errorf("parse kubeconfig: %w", err)
		}
		return clientcmd.NewDefaultClientConfig(*raw, overrides).ClientConfig()
	}

	if path == "" && kubeContext == "" {
		cfg, err := rest.InClusterConfig()
		if err == nil {
			return cfg, nil
		}
		if !errors.Is(err, rest.ErrNotInCluster) {
			return nil, err
		}
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

// Clientset builds a Kubernetes clientset; see RestConfig for the arguments.
func Clientset(path string, data []byte, kubeContext string) (kubernetes.Interface, *rest.Config, error) {
	cfg, err := RestConfig(path, data, kubeContext)
	if err != nil {
		return nil, nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	return cs, cfg, nil
}

// Contexts lists the contexts in a kubeconfig (from data if non-empty, else
// from path, else the default loading rules) and returns the current context.
func Contexts(path string, data []byte) ([]Context, string, error) {
	var (
		raw *clientcmdapi.Config
		err error
	)
	switch {
	case len(data) > 0:
		raw, err = clientcmd.Load(data)
	case path != "":
		raw, err = clientcmd.LoadFromFile(path)
	default:
		raw, err = clientcmd.NewDefaultClientConfigLoadingRules().Load()
	}
	if err != nil {
		return nil, "", fmt.Errorf("read kubeconfig: %w", err)
	}

	out := make([]Context, 0, len(raw.Contexts))
	for name, c := range raw.Contexts {
		ctx := Context{Name: name, Cluster: c.Cluster, Namespace: c.Namespace}
		if cl, ok := raw.Clusters[c.Cluster]; ok {
			ctx.Server = cl.Server
		}
		out = append(out, ctx)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, raw.CurrentContext, nil
}
