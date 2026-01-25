# Arshes CLI

CLI tool for editing Arshes shaders from your computer.

## Installation

```bash
go install github.com/shivaduke28/arshes-cli/cmd/arshes@latest
```

## Usage

### 1. Start the Server on Your Computer

```bash
# Start server (creates a new shader file automatically)
arshes serve

# Or specify an existing shader file
arshes serve shader.slang

# With custom port
arshes serve --port 9000

# Enable logging to arshes.log
arshes serve --log
```

If no file is specified, a new file with timestamp will be created (e.g., `shader_20260125200800.slang`).

The server will display its IP address and port:
```
Created new shader file: shader_20260125200800.slang
Server listening on 192.168.1.5:8080
Waiting for connection...
Watching shader_20260125200800.slang for changes
Press Ctrl+C to stop.
```

### 2. Connect from iPhone

1. Open Arshes on your iPhone
2. Open any shader in the editor
3. Tap the Wi-Fi icon in the toolbar
4. Enter the server address (e.g., `192.168.1.5:8080`)
5. Tap Connect

### 3. Edit and Preview

Now you can edit the shader file on your computer using any text editor (VS Code, Vim, etc.). Every time you save the file, it will be automatically sent to your iPhone for compilation and preview.

## Commands

### `serve`

Start a WebSocket server and watch a shader file for changes.

```bash
arshes serve [file] [flags]

Arguments:
  file             Shader file to watch (optional, auto-generated if not specified)

Flags:
  -p, --port int   Server port (default 8080)
      --log        Enable logging to arshes.log
```

## Example Shader

```hlsl
import Arshes;

[shader("fragment")]
float4 fragmentMain(float2 uv : TEXCOORD) : SV_Target {
    return float4(uv, 0.5, 1.0);
}
```

## Protocol

Arshes CLI communicates with the iOS app via WebSocket using JSON messages.

### Endpoint

```
ws://<server-ip>:<port>/
```

### Message Format

All messages are JSON objects with `type` and optional `payload` fields:

```json
{
  "type": "<message-type>",
  "payload": { ... }
}
```

### Server → Client

| Type | Payload | Description |
|------|---------|-------------|
| `updateShader` | `{ "code": string }` | Sends updated shader code to the client |

### Client → Server

| Type | Payload | Description |
|------|---------|-------------|
| `syncShader` | `{ "code": string }` | Sends the current shader code to the server |
| `compileResult` | `{ "success": bool, "error"?: string }` | Reports shader compilation result |

### Example Messages

**Server sending shader update:**
```json
{
  "type": "updateShader",
  "payload": {
    "code": "import Arshes;\n..."
  }
}
```

**Client reporting compile success:**
```json
{
  "type": "compileResult",
  "payload": {
    "success": true
  }
}
```

**Client reporting compile error:**
```json
{
  "type": "compileResult",
  "payload": {
    "success": false,
    "error": "Syntax error at line 10"
  }
}
```

## Requirements

- Go 1.21 or later (for building)
- Arshes iOS app
- Same local network (Wi-Fi) as your iPhone
