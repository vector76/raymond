# Raymond Daemon Server

The `raymond serve` command starts a long-running daemon that discovers workflows from configured directories and exposes them to clients via an HTTP API and/or MCP (Model Context Protocol) tool interface.

## Usage

```
raymond serve --root <dir> [--root <dir2> ...] [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--root` | string slice | (required) | One or more directories to scan for workflows. Each root is scanned for subdirectories and zip archives containing `workflow.yaml` manifests. |
| `--port` | int | 8080 | Port for the HTTP API server. |
| `--mcp` | bool | false | Enable the MCP transport interface. |
| `--no-http` | bool | false | Disable the HTTP server entirely. Requires `--mcp` — at least one transport must be active. |
| `--workdir` | string | (none) | Default working directory for workflow runs. |

### Examples

Start the daemon scanning a single root:

```
raymond serve --root ./workflows
```

Scan multiple roots with a custom port:

```
raymond serve --root ./workflows --root /opt/shared-workflows --port 9090
```

MCP-only mode (no HTTP server):

```
raymond serve --root ./workflows --mcp --no-http
```

## Workflow Registry

The daemon's workflow registry is responsible for discovering and indexing available workflows at startup (and on rescan). The discovery process:

1. Iterates over each configured `--root` directory.
2. For each immediate child entry:
   - **Directories**: checks for a `workflow.yaml` manifest file inside the directory.
   - **Zip archives** (`.zip` files): checks for a `workflow.yaml` entry inside the archive.
3. Parses the manifest and extracts metadata (ID, name, description, input schema, default budget, human-input requirements).
4. Workflows without a valid `workflow.yaml` manifest are silently skipped.

The registry supports rescanning at runtime to pick up newly added or removed workflows without restarting the daemon.

### Manifest requirements

For a workflow to be discoverable by the daemon, it must include a `workflow.yaml` manifest with at least the `id` field. The manifest is distinct from YAML scope files (which define inline states) — it is a metadata descriptor. See the `internal/manifest` package for the full schema.

## HTTP API

The HTTP API provides a RESTful interface for listing, inspecting, and running workflows.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/workflows` | List all discovered workflows with metadata. |
| `GET` | `/workflows/{id}` | Get details for a specific workflow. |
| `POST` | `/workflows/{id}/run` | Start a workflow run. Accepts JSON body with input parameters and optional budget/model overrides. |
| `GET` | `/runs/{id}` | Get the status of a running or completed workflow. |
| `POST` | `/runs/{id}/resume` | Resume a paused workflow run. |
| `POST` | `/rescan` | Trigger a registry rescan to discover new workflows. |

## MCP Tool Interface

When `--mcp` is enabled, the daemon exposes workflows as MCP tools, allowing AI agents to discover and invoke workflows through the standard MCP protocol.

Each discovered workflow is registered as an MCP tool with:

- **Tool name**: the workflow's manifest `id`.
- **Description**: the workflow's manifest `description`.
- **Input schema**: derived from the manifest's `input_schema` field.

The MCP transport operates over stdio, making it compatible with standard MCP client implementations.
