package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/harvester/harvester-mcp-server/pkg/version"

	"github.com/harvester/harvester-mcp-server/internal/middleware"
	"github.com/harvester/harvester-mcp-server/pkg/client"
	"github.com/harvester/harvester-mcp-server/pkg/toolsets"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/dynamiclistener"
	"github.com/rancher/dynamiclistener/server"
	"github.com/rancher/wrangler/pkg/generated/controllers/core"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"k8s.io/client-go/rest"
)

const (
	tlsName       = "harvester-mcp-server.cattle-ai-agent-system.svc"
	certNamespace = "cattle-ai-agent-system"
	certName      = "harvester-mcp-tls"
	caName        = "harvester-mcp-ca"
)

var (
	port           int
	insecure       bool
	readOnly       bool
	authzServerURL string
	jwksURL        string
	resourceURL    string

	kubeconfigs    []string
	inClusterName  string
	directAuth     string
	disableRancher bool
	apiKeyFile     string
	tlsCertFile    string
	tlsKeyFile     string
)

// apiKeyEnv is the environment variable holding the shared API key.
const apiKeyEnv = "MCP_API_KEY"

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP server",
	Long:  `Start the MCP server to handle requests from the Rancher AI agent`,
	RunE:  runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)

	serveCmd.Flags().IntVar(&port, "port", 9092, "Port to listen on")
	serveCmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS verification")
	serveCmd.Flags().BoolVar(&readOnly, "read-only", false, "Only register read-only tools")

	serveCmd.Flags().StringVar(&authzServerURL, "authz-server-url", "", "Authorization Server URL - used to generate the OIDC urls")
	serveCmd.Flags().StringVar(&jwksURL, "jwks-url", "", "JWKS URL - from the OAuth2 server")
	serveCmd.Flags().StringVar(&resourceURL, "resource-url", "", "Resource URL for this server - this should be the address to access the MCP server")

	serveCmd.Flags().StringSliceVar(&kubeconfigs, "kubeconfig", nil,
		"Kubeconfig file(s) whose contexts are served as directly connected clusters (cluster name = context name). Repeatable")
	serveCmd.Flags().StringVar(&inClusterName, "in-cluster-name", "",
		"Serve the cluster this server runs in as a direct cluster with this name")
	serveCmd.Flags().StringVar(&directAuth, "direct-auth", string(client.DirectAuthPassthrough),
		"Credentials for direct clusters: 'passthrough' (use the caller's token) or 'kubeconfig' (use the kubeconfig/service account credentials)")
	serveCmd.Flags().BoolVar(&disableRancher, "disable-rancher", false, "Do not use Rancher; serve direct clusters only")
	serveCmd.Flags().StringVar(&apiKeyFile, "api-key-file", "",
		"File containing a shared API key that callers must send in the X-MCP-API-Key header (alternatively set $"+apiKeyEnv+")")
	serveCmd.Flags().StringVar(&tlsCertFile, "tls-cert-file", "", "Serve TLS with this certificate instead of the in-cluster dynamic listener")
	serveCmd.Flags().StringVar(&tlsKeyFile, "tls-key-file", "", "Private key for --tls-cert-file")
}

// loadAPIKey returns the API key from --api-key-file or $MCP_API_KEY.
func loadAPIKey() (string, error) {
	if apiKeyFile != "" {
		b, err := os.ReadFile(apiKeyFile)
		if err != nil {
			return "", fmt.Errorf("reading api key file: %w", err)
		}
		key := strings.TrimSpace(string(b))
		if key == "" {
			return "", fmt.Errorf("api key file %s is empty", apiKeyFile)
		}
		return key, nil
	}
	return os.Getenv(apiKeyEnv), nil
}

// validateDirectAuth refuses configurations where any caller would act with
// the server's own credentials without authenticating to the MCP endpoint.
func validateDirectAuth(mode client.DirectAuthMode, hasDirect bool, oauthEnabled bool, apiKey string) error {
	if mode != client.DirectAuthKubeconfig || !hasDirect {
		return nil
	}
	if !oauthEnabled && apiKey == "" {
		return fmt.Errorf("--direct-auth=kubeconfig gives every caller the kubeconfig's permissions: " +
			"protect the endpoint with OAuth (--authz-server-url) or an API key (--api-key-file / $" + apiKeyEnv + ")")
	}
	return nil
}

func runServe(cmd *cobra.Command, args []string) error {
	zap.L().Info("Starting MCP Server", zap.String("version", version.GetVersion()), zap.Int("port", port), zap.Bool("insecure", insecure), zap.Bool("read-only", readOnly))
	if (tlsCertFile == "") != (tlsKeyFile == "") {
		return fmt.Errorf("--tls-cert-file and --tls-key-file must be set together")
	}
	authMode, err := client.ParseDirectAuthMode(directAuth)
	if err != nil {
		return err
	}
	apiKey, err := loadAPIKey()
	if err != nil {
		return err
	}
	hasDirect := len(kubeconfigs) > 0 || inClusterName != ""
	if err := validateDirectAuth(authMode, hasDirect, authzServerURL != "", apiKey); err != nil {
		return err
	}
	if authMode == client.DirectAuthPassthrough && hasDirect && authzServerURL != "" {
		zap.L().Warn("OAuth is enabled with --direct-auth=passthrough: OAuth access tokens are forwarded to direct clusters, " +
			"which usually do not accept them; use --direct-auth=kubeconfig for direct clusters behind OAuth")
	}
	if authMode == client.DirectAuthKubeconfig && hasDirect && !readOnly {
		zap.L().Warn("direct clusters use the kubeconfig's credentials and write tools are enabled; consider --read-only")
	}

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "harvester mcp server", Version: "v1.0.0"}, nil)
	client, err := client.NewClientWithOptions(client.Options{
		Insecure:       insecure,
		AuthzServerURL: authzServerURL,
		DisableRancher: disableRancher,
		Kubeconfigs:    kubeconfigs,
		InClusterName:  inClusterName,
		DirectAuth:     authMode,
	})
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	zap.L().Info("cluster access",
		zap.Bool("rancher", client.RancherEnabled()),
		zap.Strings("directClusters", client.DirectClusterNames()),
		zap.String("directAuth", string(authMode)),
		zap.Bool("apiKey", apiKey != ""))

	toolsets.AddAllTools(client, mcpServer, readOnly)

	zap.L().Info("read-only mode", zap.Bool("enabled", readOnly))

	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{})

	oauthConfig := middleware.NewOAuthConfig(authzServerURL, jwksURL, resourceURL, []string{"offline_access", "harvester:mcp"})
	if insecure {
		oauthConfig.InsecureTLS = true
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", oauthConfig.HandleProtectedResourceMetadata)
	mux.Handle("/", middleware.APIKeyMiddleware(apiKey, oauthConfig.OAuthMiddleware(handler)))

	if err := oauthConfig.LoadJWKS(cmd.Context()); err != nil {
		log.Fatalf("failed to load JWKS: %s", err)
	}

	if insecure {
		return startInsecureServer(mux)
	}
	if tlsCertFile != "" {
		return startFileTLSServer(mux)
	}

	return startTLSServer(mux)
}

// startFileTLSServer serves TLS with a certificate from disk, for running
// outside a Kubernetes cluster.
func startFileTLSServer(handler http.Handler) error {
	zap.L().Info("MCP Server started!", zap.Int("port", port), zap.String("tlsCertFile", tlsCertFile))
	srv := &http.Server{
		Addr:      fmt.Sprintf(":%d", port),
		Handler:   handler,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return srv.ListenAndServeTLS(tlsCertFile, tlsKeyFile)
}

func startInsecureServer(handler http.Handler) error {
	zap.L().Info("MCP Server started!", zap.Int("port", port), zap.Bool("insecure", true))

	addr := fmt.Sprintf(":%d", port)
	return http.ListenAndServe(addr, handler)
}

func startTLSServer(handler http.Handler) error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("error creating in-cluster config: %v", err)
	}
	factory, err := core.NewFactoryFromConfig(config)
	if err != nil {
		return fmt.Errorf("creating factory: %v", err)
	}

	ctx := context.Background()
	err = server.ListenAndServe(ctx, port, 0, handler, &server.ListenOpts{
		Secrets:       factory.Core().V1().Secret(),
		CertNamespace: certNamespace,
		CertName:      certName,
		CAName:        caName,
		TLSListenerConfig: dynamiclistener.Config{
			SANs: []string{
				tlsName,
			},
			FilterCN: dynamiclistener.OnlyAllow(tlsName),
			TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				CipherSuites: []uint16{
					tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
					tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
					tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
					tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
					tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
					tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				},
				ClientAuth: tls.RequestClientCert,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("creating tls server: %v", err)
	}

	zap.L().Info("MCP Server with TLS started!", zap.Int("port", port))
	<-ctx.Done()

	return ctx.Err()
}
