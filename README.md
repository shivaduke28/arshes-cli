# Arshes CLI

A CLI tool that works with the [Arshes](https://apps.apple.com/app/arshes/id6740044632) iOS app to edit and preview shaders from your PC. Supports file-watching mode (`serve`) and MCP server mode (`mcp`) for AI agent integration.

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
2. Enter the server address (e.g., `192.168.1.5:8080`) and connect

Once connected, saving the shader file on your PC automatically sends it to iPhone for compilation and preview.

### Flags

| Flag | Description |
|------|-------------|
| `-p, --port int` | Server port (default: 8080) |
| `--log` | Enable logging to `arshes.log` |

## MCP

Start an MCP (Model Context Protocol) server via stdio with a WebSocket bridge to iPhone. This allows AI agents like Claude Code to compile and preview shaders on the connected iPhone.

```bash
arshes mcp

# Custom port
arshes mcp --port 9000
```

### MCP Tools

| Tool | Description |
|------|-------------|
| `compile_shader` | Send shader code to iPhone for compilation. Accepts `code` (inline) or `file` (path to .slang file). Optionally save rendered image to `image` path. |
| `get_shader` | Get the last synced shader code from iPhone. |
| `get_shader_spec` | Get the Slang shader API specification (available uniforms, parameter attributes, entry point signature). |
| `get_status` | Get iPhone connection status and WebSocket server address. |

### Configuration

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

## Example Shader

```hlsl
import Arshes;

[shader("fragment")]
float4 fragmentMain(float2 uv : TEXCOORD) : SV_Target {
    return float4(uv, 0.5, 1.0);
}
```
