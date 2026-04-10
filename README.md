# Arshes CLI

A CLI tool that works with the [Arshes](https://x.com/arshes_net) iOS app to edit and preview shaders from your PC. Supports file-watching mode (`serve`) and MCP server mode (`mcp`) for AI agent integration.

## Installation

```bash
go install github.com/shivaduke28/arshes-cli/cmd/arshes@latest
```

### Requirements

- Go 1.24 or later
- Arshes iOS app
- PC and iPhone on the same local network (Wi-Fi)

## Serve

Start a WebSocket server that watches a shader file and sends updates to iPhone in real-time.

```bash
# Start server (auto-generates a new shader file)
arshes serve

# Specify an existing shader file
arshes serve shader.slang

# Custom port
arshes serve --port 9000

# Enable logging to arshes.log
arshes serve --log
```

If no file is specified, a timestamped file (e.g., `shader_20260125200800.slang`) is created automatically.

### Connecting from iPhone

1. Open the Remote Editor feature in the Arshes iOS app
2. Enter the server address (e.g., `192.168.1.5:10080`) and connect

Once connected, saving the shader file on your PC automatically sends it to iPhone for compilation and preview.

### Flags

| Flag | Description |
|------|-------------|
| `-p, --port int` | Server port (default: 10080) |
| `--log` | Enable logging to `arshes.log` |

## MCP

Start an MCP (Model Context Protocol) server with a WebSocket bridge to iPhone. This allows AI agents like Claude Code to compile and preview shaders on the connected iPhone.

```bash
arshes mcp

# Custom port
arshes mcp --port 9000

# Use Streamable HTTP transport (for remote deployment)
arshes mcp --transport http
```

### Transport Modes

| Mode | Description |
|------|-------------|
| `stdio` (default) | Communicates via stdin/stdout. The AI agent launches and manages the process. |
| `http` | Communicates via Streamable HTTP. Both the MCP endpoint (`/mcp`) and the WebSocket endpoint (`/`) are served on the same port. |

### MCP Tools

| Tool | Description |
|------|-------------|
| `compile_shader` | Send shader code to iPhone for compilation. Accepts `code` (inline) or `file` (path to .slang file). Optionally save rendered image to `image` path. |
| `get_shader` | Get the last synced shader code from iPhone. |
| `get_shader_spec` | Get the Slang shader API specification (available uniforms, parameter attributes, entry point signature). |
| `get_status` | Get iPhone connection status and WebSocket server address. |

### Configuration

**stdio (default):**

Add to your `.mcp.json`:

```json
{
  "mcpServers": {
    "arshes": {
      "command": "arshes",
      "args": ["mcp"]
    }
  }
}
```

**Streamable HTTP:**

Start the server first by `arshes mcp --transport http`, then add to your `.mcp.json`:

```json
{
  "mcpServers": {
    "arshes": {
      "type": "http",
      "url": "http://localhost:10080/mcp"
    }
  }
}
```

## Example Shader

```hlsl
[shader("fragment")]
float4 fragmentMain(float2 uv : TEXCOORD) : SV_Target {
    return float4(uv, 0.5, 1.0);
}
```
