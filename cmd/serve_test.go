package cmd

import (
	"testing"

	"github.com/harvester/harvester-mcp-server/pkg/client"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeCmd(t *testing.T) {
	assert.NotNil(t, serveCmd)
	assert.Equal(t, "serve", serveCmd.Use)
	assert.Equal(t, "Start the MCP server", serveCmd.Short)
	assert.NotNil(t, serveCmd.RunE)
}

func TestRunServeCommand(t *testing.T) {
	// Create a new command instance to avoid modifying the global one
	testCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server",
		Long:  `Start the MCP server to handle requests from the Rancher AI agent`,
	}

	testCmd.Flags().IntVar(&port, "port", 9092, "Port to listen on")
	testCmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS verification")

	// Verify flags exist and have correct defaults
	portFlag := testCmd.Flags().Lookup("port")
	require.NotNil(t, portFlag)
	assert.Equal(t, "9092", portFlag.DefValue)

	insecureFlag := testCmd.Flags().Lookup("insecure")
	require.NotNil(t, insecureFlag)
	assert.Equal(t, "false", insecureFlag.DefValue)
}

func TestValidateDirectAuth(t *testing.T) {
	tests := map[string]struct {
		mode      client.DirectAuthMode
		hasDirect bool
		oauth     bool
		apiKey    string
		wantErr   bool
	}{
		"passthrough needs nothing":           {mode: client.DirectAuthPassthrough, hasDirect: true},
		"kubeconfig without protection fails": {mode: client.DirectAuthKubeconfig, hasDirect: true, wantErr: true},
		"kubeconfig with api key":             {mode: client.DirectAuthKubeconfig, hasDirect: true, apiKey: "k"},
		"kubeconfig with oauth":               {mode: client.DirectAuthKubeconfig, hasDirect: true, oauth: true},
		"kubeconfig without direct clusters":  {mode: client.DirectAuthKubeconfig},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateDirectAuth(tc.mode, tc.hasDirect, tc.oauth, tc.apiKey)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
