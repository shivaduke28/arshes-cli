# Arshes CLI

[English](README.md) | 日本語

[Arshes](https://arshes.shivaduke28.net/ja/) アプリと連携し、PC からシェーダーを編集・プレビューするための CLI ツールです。ファイル監視モード（`serve`）と、AI エージェント連携用の MCP サーバーモード（`mcp`）に対応しています。

Arshes CLI は [Arshes Remote Editor Protocol v1](https://arshes.shivaduke28.net/ja/protocol/) を実装しており、WebSocket 経由で Arshes アプリと通信します。

## インストール

```bash
go install github.com/shivaduke28/arshes-cli/cmd/arshes@latest
```

### 必要なもの

- Go 1.24 以降
- Arshes アプリ
- PC とデバイスが同じローカルネットワーク（Wi-Fi）に接続されていること

## Serve

WebSocket サーバーを起動してシェーダーファイルを監視し、変更を接続中のデバイスへリアルタイムに送信します。

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

ファイルを指定しない場合は、タイムスタンプ付きのファイル（例: `shader_20260125200800.slang`）が自動で作成されます。

### アプリからの接続

1. Arshes アプリで Remote Editor 機能を開く
2. サーバーのアドレス（例: `192.168.1.5:10080`）を入力して接続する

アクセスを制限したい場合は、サーバー側で `--token`（または環境変数 `ARSHES_TOKEN`）を設定し、Arshes アプリにも同じトークンを設定してください。トークンは WebSocket のハンドシェイク時に検証されます。

接続後は、PC 上でシェーダーファイルを保存するたびに、接続中のデバイスへ自動的に送信され、コンパイルとプレビューが行われます。

### フラグ

| フラグ | 説明 |
|------|-------------|
| `-p, --port int` | サーバーのポート（デフォルト: 10080） |
| `--token string` | クライアント接続用の認証トークン（環境変数 `ARSHES_TOKEN` でも設定可能） |
| `--log` | `arshes.log` へのログ出力を有効にする |

## MCP

Arshes アプリへの WebSocket ブリッジを備えた MCP（Model Context Protocol）サーバーを起動します。Claude Code などの AI エージェントが、接続中のデバイス上でシェーダーをコンパイル・プレビューできるようになります。

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

### トランスポートモード

| モード | 説明 |
|------|-------------|
| `stdio`（デフォルト） | 標準入出力で通信します。AI エージェントがプロセスを起動・管理します。 |
| `http` | Streamable HTTP で通信します。MCP エンドポイント（`/mcp`）と WebSocket エンドポイント（`/`）の両方が同じポートで提供されます。 |

### MCP ツール

| ツール | 説明 |
|------|-------------|
| `compile_shader` | シェーダーコードを接続中のデバイスへ送信してコンパイルします。`code`（インライン）または `file`（.slang ファイルへのパス）を受け付けます。`image` にパスを指定すると、レンダリング結果の画像を保存できます。 |
| `get_shader` | 接続中のデバイスから最後に同期されたシェーダーコードを取得します。 |
| `get_shader_spec` | Slang シェーダーの API 仕様（利用可能な uniform、パラメータ属性、エントリーポイントのシグネチャ）を取得します。 |
| `get_status` | デバイスの接続状態と WebSocket サーバーのアドレスを取得します。 |

### 設定

**stdio（デフォルト）:**

`.mcp.json` に以下を追加します:

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

先に `arshes mcp --transport http` でサーバーを起動してから、`.mcp.json` に以下を追加します:

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

**認証付き Streamable HTTP:**

`--token` を設定した場合、MCP エンドポイントには `Authorization: Bearer <token>` ヘッダーが必要です:

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

## シェーダーの例

```hlsl
[shader("fragment")]
float4 fragmentMain(float2 uv : TEXCOORD) : SV_Target {
    return float4(uv, 0.5, 1.0);
}
```
