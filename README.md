# Arshes CLI

English | [日本語](README.ja.md)

A CLI tool that works with the [Arshes](https://arshes.shivaduke28.net/) app to edit and preview shaders from your PC. Supports file-watching mode (`serve`) and MCP server mode (`mcp`) for AI agent integration.

Arshes CLI implements the [Arshes Remote Editor Protocol v2](https://arshes.shivaduke28.net/protocol/v2/) (JSON-RPC 2.0 over WebSocket) for communication with the Arshes app. It speaks only v2 and cannot connect to app versions that support only v1.

## Installation

```bash
go install github.com/shivaduke28/arshes-cli/cmd/arshes@latest
```

### Requirements

- Go 1.24 or later
- Arshes app
- PC and your device on the same local network (Wi-Fi)

## Serve

Start a WebSocket server that watches a shader file and sends updates to the connected device in real-time.

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

### Connecting from the App

1. Open the Remote Editor feature in the Arshes app
2. Enter the server address (e.g., `192.168.1.5:10080`) and connect

To restrict access, set `--token` (or `ARSHES_TOKEN` env var) on the server and configure the same token in the Arshes app. The token is verified during the WebSocket handshake.

Once connected, saving the shader file on your PC automatically sends it to the connected device for compilation and preview.

### Flags

| Flag | Description |
|------|-------------|
| `-p, --port int` | Server port (default: 10080) |
| `--token string` | Authentication token for client connections (can also be set via `ARSHES_TOKEN` env var) |
| `--log` | Enable logging to `arshes.log` |

## MCP

Start an MCP (Model Context Protocol) server with a WebSocket bridge to the Arshes app. This allows AI agents like Claude Code to compile shaders, capture the rendered image, and adjust uniforms and other settings on the connected device.

```bash
arshes mcp

# Custom port
arshes mcp --port 9000

# Use Streamable HTTP transport (for remote deployment)
arshes mcp --transport http

# With token authentication (recommended for remote deployment)
arshes mcp --transport http --token mytoken
# Or via environment variable
ARSHES_TOKEN=mytoken arshes mcp --transport http
```

### Transport Modes

| Mode | Description |
|------|-------------|
| `stdio` (default) | Communicates via stdin/stdout. The AI agent launches and manages the process. |
| `http` | Communicates via Streamable HTTP. Both the MCP endpoint (`/mcp`) and the WebSocket endpoint (`/`) are served on the same port. |

### MCP Tools

| Tool | Description |
|------|-------------|
| `compile_shader` | Send shader code to the connected device for compilation. Accepts `code` (inline) or `file` (path to .slang file). Returns whether it succeeded, the compiler error on failure, and the compile time on the device. Does not return an image. |
| `capture_image` | Capture the next rendered frame as a JPEG (long edge up to 512 px). Returned inline, or saved to the `image` path if given. |
| `get_uniforms` | Get the current values of the custom uniforms (`range`, `rgb`, `rgba`, `toggle`) of the shader being rendered. |
| `set_uniforms` | Set custom uniform values with `values` (an object keyed by uniform name). If any value is invalid, nothing is changed. |
| `set_aspect_ratio` | Change the camera view's aspect ratio (`1:1`, `4:3`, `16:9`, `2.35:1`, `full`). |
| `get_diagnostics` | Get rendering statistics: `fps`, `gpuTimeMs` (if the device can measure it), and render `resolution`. |
| `set_touch` | Set the built-in `touch` uniform with `x`, `y` (0–1, origin at top left) and `z` (1 while touching, 0 when released). |
| `get_shader` | Get the last synced shader code from the connected device. |
| `get_shader_spec` | Get the Slang shader API specification (available uniforms, parameter attributes, entry point signature). |
| `get_status` | Get device connection status and WebSocket server address. |

When multiple devices are connected, each request is sent to all of them and the first successful response is used. Changes such as `set_uniforms` apply to every connected device. `set_aspect_ratio` behaves the same as choosing the aspect ratio in the app UI.

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

**Streamable HTTP with authentication:**

When `--token` is set, the MCP endpoint requires `Authorization: Bearer <token>` header:

```json
{
  "mcpServers": {
    "arshes": {
      "type": "http",
      "url": "https://your-app.fly.dev/mcp",
      "headers": {
        "Authorization": "Bearer mytoken"
      }
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
