# Arshes CLI

CLI tool for editing Arshes shaders from your computer.

## Installation

```bash
# From source
go install github.com/shivaduke28/arshes-cli@latest

# Or build locally
cd cli
go build -o arshes
```

## Usage

### 1. Start the Server on Your Computer

```bash
# Start server and watch a shader file
arshes serve shader.slang

# With custom port
arshes serve shader.slang --port 9000
```

The server will display its IP address and port:
```
Server listening on 192.168.1.5:8080
Waiting for connection...
Watching shader.slang for changes
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
arshes serve <file> [flags]

Flags:
  -p, --port int   Server port (default 8080)
```

## Example Shader

```hlsl
import Arshes;

struct VertexOutput {
    float4 position : SV_Position;
    float2 texCoord : TEXCOORD;
};

[shader("fragment")]
float4 fragmentMain(VertexOutput input) : SV_Target {
    float2 uv = input.texCoord;
    return float4(uv, 0.5, 1.0);
}
```

## Requirements

- Go 1.21 or later (for building)
- Arshes iOS app
- Same local network (Wi-Fi) as your iPhone
