package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: lab
  cluster:
    server: https://10.0.0.10:6443
    insecure-skip-tls-verify: true
- name: prod
  cluster:
    server: https://harvester-prod.example.com/k8s/clusters/local
    insecure-skip-tls-verify: true
users:
- name: admin
  user:
    token: kubeconfig-token
- name: certuser
  user:
    client-certificate-data: Zm9v
    client-key-data: YmFy
contexts:
- name: harv-lab
  context: {cluster: lab, user: certuser}
- name: harv-prod
  context: {cluster: prod, user: admin}
current-context: harv-lab
`

func writeKubeconfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func newDirectClient(t *testing.T, mode DirectAuthMode, rancherURL string) *Client {
	t.Helper()
	direct, err := loadKubeconfigClusters([]string{writeKubeconfig(t, testKubeconfig)})
	require.NoError(t, err)
	return &Client{rancherURL: rancherURL, rancherDisabled: rancherURL == "", direct: direct, directAuth: mode, insecure: true}
}

func TestLoadKubeconfigClusters(t *testing.T) {
	path := writeKubeconfig(t, testKubeconfig)
	direct, err := loadKubeconfigClusters([]string{path})
	require.NoError(t, err)
	require.Len(t, direct, 2)
	assert.Equal(t, "https://10.0.0.10:6443", direct["harv-lab"].config.Host)
	assert.Equal(t, "https://harvester-prod.example.com/k8s/clusters/local", direct["harv-prod"].config.Host)

	_, err = loadKubeconfigClusters([]string{path, writeKubeconfig(t, testKubeconfig)})
	assert.ErrorContains(t, err, "is defined in both")

	_, err = loadKubeconfigClusters([]string{filepath.Join(t.TempDir(), "missing")})
	assert.Error(t, err)
}

func TestRestConfigForDirect(t *testing.T) {
	ctx := context.Background()

	t.Run("passthrough replaces kubeconfig credentials with the caller token", func(t *testing.T) {
		c := newDirectClient(t, DirectAuthPassthrough, "")
		cfg, err := c.restConfigFor(ctx, fakeToken, "harv-lab")
		require.NoError(t, err)
		assert.Equal(t, "https://10.0.0.10:6443", cfg.Host)
		assert.Equal(t, fakeToken, cfg.BearerToken)
		assert.Empty(t, cfg.CertData)
		assert.Empty(t, cfg.KeyData)
		assert.True(t, cfg.Insecure, "TLS verification settings are kept")

		cfg, err = c.restConfigFor(ctx, fakeToken, "harv-prod")
		require.NoError(t, err)
		assert.Equal(t, fakeToken, cfg.BearerToken, "kubeconfig token must not be used")
	})

	t.Run("passthrough requires a caller token", func(t *testing.T) {
		c := newDirectClient(t, DirectAuthPassthrough, "")
		_, err := c.restConfigFor(ctx, "", "harv-lab")
		assert.ErrorContains(t, err, "no token was provided")
	})

	t.Run("kubeconfig mode uses kubeconfig credentials", func(t *testing.T) {
		c := newDirectClient(t, DirectAuthKubeconfig, "")
		cfg, err := c.restConfigFor(ctx, "", "harv-prod")
		require.NoError(t, err)
		assert.Equal(t, "kubeconfig-token", cfg.BearerToken)

		cfg, err = c.restConfigFor(ctx, fakeToken, "harv-lab")
		require.NoError(t, err)
		assert.Equal(t, []byte("foo"), cfg.CertData)
		assert.Empty(t, cfg.BearerToken, "caller token must not be used")
	})

	t.Run("configs are copies", func(t *testing.T) {
		c := newDirectClient(t, DirectAuthKubeconfig, "")
		cfg, err := c.restConfigFor(ctx, "", "harv-prod")
		require.NoError(t, err)
		cfg.BearerToken = "mutated"
		assert.Equal(t, "kubeconfig-token", c.direct["harv-prod"].config.BearerToken)
	})

	t.Run("unknown cluster with Rancher disabled", func(t *testing.T) {
		c := newDirectClient(t, DirectAuthPassthrough, "")
		_, err := c.restConfigFor(ctx, fakeToken, "c-m-12345")
		assert.ErrorContains(t, err, "Rancher is disabled; configured direct clusters: harv-lab, harv-prod")
		_, err = c.GetClusterID(ctx, fakeToken, "c-m-12345")
		assert.Error(t, err)
	})

	t.Run("falls back to Rancher for other clusters", func(t *testing.T) {
		c := newDirectClient(t, DirectAuthPassthrough, fakeUrl)
		cfg, err := c.restConfigFor(ctx, fakeToken, "local")
		require.NoError(t, err)
		assert.Equal(t, fakeUrl+"/k8s/clusters/local", cfg.Host)
	})

	t.Run("direct cluster takes precedence over Rancher", func(t *testing.T) {
		c := newDirectClient(t, DirectAuthPassthrough, fakeUrl)
		cfg, err := c.restConfigFor(ctx, fakeToken, "harv-lab")
		require.NoError(t, err)
		assert.Equal(t, "https://10.0.0.10:6443", cfg.Host)
		id, err := c.GetClusterID(ctx, fakeToken, "harv-lab")
		require.NoError(t, err)
		assert.Equal(t, "harv-lab", id)
	})
}

func TestGetResourcesDirect(t *testing.T) {
	c := newDirectClient(t, DirectAuthPassthrough, "")
	var gotHost string
	c.DynClientCreator = func(cfg *rest.Config) (dynamic.Interface, error) {
		gotHost = cfg.Host
		return dynamicfake.NewSimpleDynamicClient(scheme(), &v1.Node{
			TypeMeta:   metav1.TypeMeta{Kind: "Node", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{Name: "node1"},
		}), nil
	}
	objs, err := c.GetResources(context.Background(), ListParams{Cluster: "harv-lab", Kind: "node", Token: fakeToken})
	require.NoError(t, err)
	require.Len(t, objs, 1)
	assert.Equal(t, "node1", objs[0].GetName())
	assert.Equal(t, "https://10.0.0.10:6443", gotHost)
}

func TestListClustersDirectOnly(t *testing.T) {
	c := newDirectClient(t, DirectAuthPassthrough, "")
	res, err := c.ListClusters(context.Background(), fakeToken, true)
	require.NoError(t, err)
	assert.Empty(t, res.RancherError)
	assert.Equal(t, []ClusterInfo{
		{Name: "harv-lab", Source: SourceDirect, Server: "https://10.0.0.10:6443"},
		{Name: "harv-prod", Source: SourceDirect, Server: "https://harvester-prod.example.com/k8s/clusters/local"},
	}, res.Clusters)
}

func TestListClustersWithRancher(t *testing.T) {
	c := newDirectClient(t, DirectAuthPassthrough, fakeUrl)
	imported := newFakeCluster("c-m-abc", "harv-imported")
	imported.SetLabels(map[string]string{"provider.cattle.io": "harvester"})
	shadowed := newFakeCluster("harv-lab", "shadowed")
	shadowed.SetLabels(map[string]string{"provider.cattle.io": "harvester"})
	c.DynClientCreator = func(cfg *rest.Config) (dynamic.Interface, error) {
		assert.Equal(t, fakeUrl+"/k8s/clusters/local", cfg.Host)
		return dynamicfake.NewSimpleDynamicClient(scheme(), imported, shadowed), nil
	}
	res, err := c.ListClusters(context.Background(), fakeToken, false)
	require.NoError(t, err)
	require.Len(t, res.Clusters, 3)
	assert.Equal(t, ClusterInfo{Name: "c-m-abc", DisplayName: "harv-imported", Source: SourceRancher, Provider: "harvester"}, res.Clusters[2])
}

func TestNewClientWithOptions(t *testing.T) {
	path := writeKubeconfig(t, testKubeconfig)

	t.Run("disable rancher without direct clusters fails", func(t *testing.T) {
		_, err := NewClientWithOptions(Options{Insecure: true, DisableRancher: true})
		assert.Error(t, err)
	})

	t.Run("direct only", func(t *testing.T) {
		c, err := NewClientWithOptions(Options{Insecure: true, DisableRancher: true, Kubeconfigs: []string{path}})
		require.NoError(t, err)
		assert.False(t, c.RancherEnabled())
		assert.Equal(t, []string{"harv-lab", "harv-prod"}, c.DirectClusterNames())
	})

	t.Run("falls back to direct only when Rancher cannot be discovered", func(t *testing.T) {
		t.Setenv("RANCHER_URL", "")
		t.Setenv("KUBERNETES_SERVICE_HOST", "")
		c, err := NewClientWithOptions(Options{Insecure: true, Kubeconfigs: []string{path}})
		require.NoError(t, err)
		assert.False(t, c.RancherEnabled())
	})

	t.Run("hybrid with RANCHER_URL", func(t *testing.T) {
		t.Setenv("RANCHER_URL", fakeUrl)
		c, err := NewClientWithOptions(Options{Insecure: true, Kubeconfigs: []string{path}})
		require.NoError(t, err)
		assert.True(t, c.RancherEnabled())
		assert.Equal(t, fakeUrl, c.RancherURL())
		assert.Len(t, c.DirectClusterNames(), 2)
	})

	t.Run("invalid auth mode", func(t *testing.T) {
		_, err := NewClientWithOptions(Options{Insecure: true, Kubeconfigs: []string{path}, DirectAuth: "bogus"})
		assert.Error(t, err)
	})

	t.Run("no Rancher and no direct clusters keeps the old error", func(t *testing.T) {
		t.Setenv("RANCHER_URL", "")
		t.Setenv("KUBERNETES_SERVICE_HOST", "")
		_, err := NewClientWithOptions(Options{Insecure: true})
		assert.ErrorContains(t, err, "fetching internal-server-url")
	})
}
