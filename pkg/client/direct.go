package client

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/harvester/harvester-mcp-server/pkg/converter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// DirectAuthMode controls which credentials are used for directly connected clusters.
type DirectAuthMode string

const (
	// DirectAuthPassthrough keeps the server URL and TLS settings from the
	// kubeconfig, drops its credentials, and authenticates with the token sent
	// by the MCP caller. Each caller keeps its own RBAC permissions.
	DirectAuthPassthrough DirectAuthMode = "passthrough"

	// DirectAuthKubeconfig uses the credentials stored in the kubeconfig (or
	// the pod's service account for the in-cluster target). Every MCP caller
	// acts with these credentials, so the MCP endpoint itself must be protected.
	DirectAuthKubeconfig DirectAuthMode = "kubeconfig"
)

// ParseDirectAuthMode validates a --direct-auth value.
func ParseDirectAuthMode(s string) (DirectAuthMode, error) {
	switch m := DirectAuthMode(s); m {
	case DirectAuthPassthrough, DirectAuthKubeconfig:
		return m, nil
	case "":
		return DirectAuthPassthrough, nil
	default:
		return "", fmt.Errorf("invalid direct auth mode %q (valid: %s, %s)", s, DirectAuthPassthrough, DirectAuthKubeconfig)
	}
}

// Source values reported by ListClusters.
const (
	SourceDirect  = "direct"
	SourceRancher = "rancher"
)

// directCluster is a cluster reached without going through Rancher.
type directCluster struct {
	name   string
	origin string // kubeconfig path, or "in-cluster"
	config *rest.Config
}

// restConfig returns the rest.Config to use for a request with the given caller token.
func (d *directCluster) restConfig(mode DirectAuthMode, token string) (*rest.Config, error) {
	if mode == DirectAuthKubeconfig {
		return rest.CopyConfig(d.config), nil
	}
	if token == "" {
		return nil, fmt.Errorf("cluster %q uses passthrough auth but no token was provided by the caller", d.name)
	}
	// AnonymousClientConfig keeps host and TLS server verification settings but
	// drops every credential (token, token file, client certs, exec/auth providers).
	cfg := rest.AnonymousClientConfig(d.config)
	cfg.BearerToken = token
	return cfg, nil
}

// loadKubeconfigClusters returns one directCluster per context in the given kubeconfig files.
func loadKubeconfigClusters(paths []string) (map[string]*directCluster, error) {
	out := map[string]*directCluster{}
	for _, path := range paths {
		raw, err := clientcmd.LoadFromFile(path)
		if err != nil {
			return nil, fmt.Errorf("loading kubeconfig %s: %w", path, err)
		}
		if len(raw.Contexts) == 0 {
			return nil, fmt.Errorf("kubeconfig %s has no contexts", path)
		}
		for name := range raw.Contexts {
			if prev, dup := out[name]; dup {
				return nil, fmt.Errorf("context %q is defined in both %s and %s", name, prev.origin, path)
			}
			cfg, err := clientcmd.NewNonInteractiveClientConfig(*raw, name, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
			if err != nil {
				return nil, fmt.Errorf("kubeconfig %s, context %q: %w", path, name, err)
			}
			out[name] = &directCluster{name: name, origin: path, config: cfg}
		}
	}
	return out, nil
}

// isDirect reports whether cluster is a directly connected cluster.
func (c *Client) isDirect(cluster string) bool {
	_, ok := c.direct[cluster]
	return ok
}

// RancherEnabled reports whether clusters can be resolved through Rancher.
func (c *Client) RancherEnabled() bool {
	return !c.rancherDisabled
}

// DirectClusterNames returns the names of the directly connected clusters.
func (c *Client) DirectClusterNames() []string {
	names := make([]string, 0, len(c.direct))
	for n := range c.direct {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// restConfigFor returns the rest.Config for a cluster. Directly connected
// clusters take precedence over Rancher clusters with the same name.
func (c *Client) restConfigFor(ctx context.Context, token, cluster string) (*rest.Config, error) {
	if d, ok := c.direct[cluster]; ok {
		return d.restConfig(c.directAuth, token)
	}
	if !c.RancherEnabled() {
		return nil, c.unknownClusterError(cluster)
	}
	clusterID, err := c.GetClusterID(ctx, token, cluster)
	if err != nil {
		return nil, err
	}
	return c.CreateRestConfig(token, clusterID)
}

func (c *Client) unknownClusterError(cluster string) error {
	names := c.DirectClusterNames()
	if len(names) == 0 {
		return fmt.Errorf("cluster '%s' not found: Rancher is disabled and no direct clusters are configured", cluster)
	}
	return fmt.Errorf("cluster '%s' not found: Rancher is disabled; configured direct clusters: %s", cluster, strings.Join(names, ", "))
}

// ClusterInfo describes a cluster reachable through this server.
type ClusterInfo struct {
	// Name is the value to pass as the "cluster" argument of other tools.
	Name string `json:"name"`
	// DisplayName is the Rancher display name, if any.
	DisplayName string `json:"displayName,omitempty"`
	// Source is "direct" or "rancher".
	Source string `json:"source"`
	// Provider is the Rancher provider label (e.g. "harvester"), if known.
	Provider string `json:"provider,omitempty"`
	// Server is the API server URL of a direct cluster.
	Server string `json:"server,omitempty"`
}

// ClustersResult is the result of ListClusters.
type ClustersResult struct {
	Clusters []ClusterInfo `json:"clusters"`
	// RancherError is set when Rancher clusters could not be listed.
	RancherError string `json:"rancherError,omitempty"`
}

// ListClusters returns the direct clusters and, when Rancher is enabled, the
// clusters known to Rancher. With harvesterOnly, Rancher clusters are limited
// to those with the provider.cattle.io=harvester label.
func (c *Client) ListClusters(ctx context.Context, token string, harvesterOnly bool) (*ClustersResult, error) {
	res := &ClustersResult{Clusters: []ClusterInfo{}}
	for _, name := range c.DirectClusterNames() {
		res.Clusters = append(res.Clusters, ClusterInfo{Name: name, Source: SourceDirect, Server: c.direct[name].config.Host})
	}
	if !c.RancherEnabled() {
		return res, nil
	}

	cfg, err := c.CreateRestConfig(token, "local")
	if err == nil {
		var list *unstructured.UnstructuredList
		dyn, derr := c.DynClientCreator(cfg)
		if derr != nil {
			err = derr
		} else {
			opts := metav1.ListOptions{}
			if harvesterOnly {
				opts.LabelSelector = "provider.cattle.io=harvester"
			}
			list, err = dyn.Resource(converter.K8sKindsToGVRs["managementcluster"]).List(ctx, opts)
		}
		if err == nil {
			for _, item := range list.Items {
				name := item.GetName()
				if c.isDirect(name) {
					continue // shadowed by a direct cluster
				}
				dn, _, _ := unstructured.NestedString(item.Object, "spec", "displayName")
				res.Clusters = append(res.Clusters, ClusterInfo{
					Name:        name,
					DisplayName: dn,
					Source:      SourceRancher,
					Provider:    item.GetLabels()["provider.cattle.io"],
				})
			}
		}
	}
	if err != nil {
		res.RancherError = err.Error()
	}
	return res, nil
}
