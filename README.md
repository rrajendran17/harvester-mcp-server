## Harvester MCP Server

The MCP server allows the [Rancher AI agent](https://github.com/rancher-sandbox/rancher-ai-agent) to securely retrieve or update Harvester resources in Harvester clusters. It expects the Rancher token in a header, which the agent will always provide for authentication.

> [!NOTE]
> This repo was originally forked from https://github.com/rancher/rancher-ai-mcp and is now dedicated to Harvester / SUSE Virtualization.

## Overview

This Model Context Protocol (MCP) server provides a secure bridge between the Rancher AI agent and Harvester clusters, enabling AI-powered cluster management through a standardized tool interface. The server runs as a Kubernetes deployment within the Rancher environment and exposes tools for resource inspection, modification, and cluster operations.

## Architecture

### Package Structure

- **`cmd/`** - CLI commands and server initialization
  - `serve.go` - HTTP/TLS server setup with dynamic listener support
  - `root.go` - Root command configuration

- **`pkg/client/`** - Kubernetes client abstraction
  - Dynamic client wrapper with cluster ID resolution
  - Rancher API integration for cluster management
  - Support for both local and downstream cluster operations

- **`pkg/toolsets/virtualization/`** - Harvester / SUSE Virtualization tools
  - `toolsets.go` - Central registry for tool collections
  - VM, image, volume, network, and node operations

- **`pkg/response/`** - Response formatting utilities
  - Structured text and content generation for MCP responses

- **`pkg/converter/`** - Data transformation utilities
  - Group/Version/Resource (GVR) conversion helpers

### TLS & Security

The server supports two modes:

1. **TLS Mode (Production)**: Uses Rancher's dynamic listener with auto-generated certificates
   - Certificates stored as Kubernetes secrets
   - Automatic cert rotation and renewal
   - Client certificate authentication support
   - TLS 1.2+ with secure cipher suites

2. **Insecure Mode (Development)**: Plain HTTP for local testing
   - Enabled via `--insecure` flag or `INSECURE_SKIP_TLS=true`

### Available Tools

Each tool is exposed through the MCP protocol and can be invoked by the Rancher AI agent:

| Tool                          | Description                                                                       |
|-------------------------------|------------------------------------------------------------------------------------|
| `listVirtualMachines`         | Lists VirtualMachine resources on a Harvester / SUSE Virtualization cluster        |
| `listVirtualMachineImages`    | Lists VirtualMachineImage resources on a Harvester / SUSE Virtualization cluster   |
| `listVolumes`                 | Lists storage volumes (PersistentVolumeClaims) on a Harvester / SUSE Virtualization cluster |
| `listVirtualMachineNetworks`  | Lists NetworkAttachmentDefinition resources on a Harvester / SUSE Virtualization cluster |
| `listClusterNetworks`         | Lists ClusterNetwork resources on a Harvester / SUSE Virtualization cluster        |
| `listNodes`                   | Lists nodes (hosts) on a Harvester / SUSE Virtualization cluster                   |
| `inspectVolume`               | Inspects a storage volume (PersistentVolumeClaim), including Longhorn details when applicable |
| `createVirtualMachineImage`   | Creates a VirtualMachineImage resource by downloading from a URL                   |
| `createVirtualMachine`        | Creates a VirtualMachine from an existing image                                    |
| `createVirtualMachinePlan`    | Returns the manifest that would be submitted to create a VirtualMachine, without creating it |
| `listClusters`                | Lists the clusters the server can access (direct clusters and Harvester clusters imported into Rancher) |

## Cluster Access

Every tool takes a `cluster` argument. The server can reach a cluster in two ways,
and both can be used at the same time:

1. **Through Rancher (default)**: for Harvester clusters imported into Rancher.
   `cluster` is the Rancher cluster ID or display name, and requests go to
   `<rancher>/k8s/clusters/<id>` with the caller's Rancher token.
2. **Directly**: for Harvester clusters that are not imported into Rancher.
   - `--kubeconfig <file>` (repeatable): every context in the file becomes a
     cluster named after the context.
   - `--in-cluster-name <name>`: serves the cluster the server itself runs in,
     e.g. when the server is installed on a Harvester cluster.

If a direct cluster and a Rancher cluster have the same name, the direct
cluster is used. Pass `--disable-rancher` to serve direct clusters only. When
direct clusters are configured and no Rancher URL can be discovered, the server
also runs in direct-only mode.

### Credentials for direct clusters (`--direct-auth`)

| Mode | Behaviour | Typical use |
|---|---|---|
| `passthrough` (default) | Keeps the server URL and TLS settings from the kubeconfig, drops its credentials, and sends the caller's token (the `R_token` header) as the bearer token. Each caller keeps their own RBAC permissions | A kubeconfig downloaded from the Harvester UI (it points at Harvester's embedded Rancher, `/k8s/clusters/local`) with Harvester API keys as tokens, or Kubernetes service account tokens |
| `kubeconfig` | Uses the credentials in the kubeconfig, or the pod's service account for `--in-cluster-name`. Every caller acts with these credentials | Admin kubeconfigs with client certificates (e.g. RKE2 `rke2.yaml`), or OAuth mode, where caller tokens are not valid on the cluster |

Because `kubeconfig` mode gives every caller the same permissions, the server
refuses to start in that mode unless the endpoint is protected by OAuth
(`--authz-server-url`) or a shared API key (`--api-key-file` or `$MCP_API_KEY`,
sent by callers in the `X-MCP-API-Key` header). Combine it with `--read-only`
unless write tools are needed.

### Examples

```bash
# Direct only, from a workstation, using a kubeconfig downloaded from the Harvester UI
mcp serve --insecure --disable-rancher --kubeconfig ~/harvester-lab.yaml
# callers send their Harvester API key in R_token and use cluster=<context name>

# Rancher and direct clusters together, with TLS from files
RANCHER_URL=https://rancher.example.com mcp serve \
  --kubeconfig ~/harvester-lab.yaml \
  --tls-cert-file server.crt --tls-key-file server.key

# Installed on a Harvester cluster itself, using its service account
MCP_API_KEY=$(cat key) mcp serve --disable-rancher --in-cluster-name harvester \
  --direct-auth kubeconfig --read-only
```

Helm values: `rancher.enabled`, `direct.kubeconfigSecret` (with
`direct.kubeconfigSecretKey`), `direct.inClusterName`, `direct.auth` and
`apiKey.secret`. With `direct.inClusterName` and `direct.auth=kubeconfig`, the
chart creates read-only RBAC for the tools, plus write permissions unless
`mcp.readOnly` is set.

## Configuration

### Command-line Flags

```bash
--port <int>              Port to listen on (default: 9092)
--insecure                Serve plain HTTP and skip TLS verification towards Rancher (default: false)
--read-only               Only register read-only tools
--kubeconfig <file>       Kubeconfig whose contexts are served as direct clusters (repeatable)
--in-cluster-name <name>  Serve the cluster the server runs in as a direct cluster
--direct-auth <mode>      passthrough (default) or kubeconfig
--disable-rancher         Serve direct clusters only
--api-key-file <file>     Require this key in the X-MCP-API-Key header (or set $MCP_API_KEY)
--tls-cert-file <file>    Serve TLS with this certificate (with --tls-key-file), for running outside a cluster
--tls-key-file <file>     Private key for --tls-cert-file
```
