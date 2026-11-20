package virtualization

import (
	"context"

	"github.com/harvester/harvester-mcp-server/internal/middleware"
	"github.com/harvester/harvester-mcp-server/pkg/response"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// listClustersParams specifies the parameters for listing clusters.
type listClustersParams struct {
	All bool `json:"all,omitempty" jsonschema:"include every Rancher cluster instead of only Harvester clusters"`
}

// listClusters returns the clusters this server can reach: directly connected
// clusters and, when Rancher is enabled, Harvester clusters imported into Rancher.
func (t *Tools) listClusters(ctx context.Context, _ *mcp.CallToolRequest, params listClustersParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("listClusters called", zap.Bool("all", params.All))

	res, err := t.client.ListClusters(ctx, middleware.Token(ctx), !params.All)
	if err != nil {
		zap.L().Error("failed to list clusters", zap.String("tool", "listClusters"), zap.Error(err))
		return nil, nil, err
	}

	mcpResponse, err := response.CreateMcpResponseAny(res)
	if err != nil {
		zap.L().Error("failed to create mcp response", zap.String("tool", "listClusters"), zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}
