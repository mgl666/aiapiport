# aiapiport

**[English](#english) | [中文](#中文)**

---

## English

A minimal LLM API gateway written in Go. Aggregates multiple upstream providers and keys into a single OpenAI-compatible endpoint. Idle memory: **~12 MB** (vs ~200 MB for Python LiteLLM).

### Install

```bash
curl -fsSL https://raw.githubusercontent.com/mgl666/aiapiport/main/install.sh | sh
```

Or download a binary directly from [Releases](https://github.com/mgl666/aiapiport/releases).

### Configuration

**Step 1 — create the config file**

By default `aiapiport` looks for `config.yaml` in the **current working directory**. You can put it anywhere and point to it with `-config`:

```bash
aiapiport start -config /etc/aiapiport/config.yaml
```

**Step 2 — fill in the values**

Copy `config.yaml.example` to `config.yaml` and edit it:

```bash
cp config.yaml.example config.yaml
```

```yaml
server:
  listen: ":8787"          # address and port to listen on
  auth_key: "sk-change-me" # clients must send this as Bearer token

providers:
  # OpenAI-compatible upstream (OpenAI, DeepSeek, SiliconFlow, relay, etc.)
  - name: my-openai         # arbitrary name, referenced in routes
    base_url: "https://api.openai.com/v1"
    type: openai            # "openai" for any OpenAI-compatible API
    keys:
      - "sk-primary-key"
      - "sk-backup-key"     # optional — tried in order on 429/5xx/401

  # Anthropic Claude (direct API)
  - name: my-claude
    base_url: "https://api.anthropic.com"
    type: anthropic         # "anthropic" for Claude direct API
    keys:
      - "sk-ant-xxxxxxxxxx"

routes:
  # map the model name clients send → provider name(s) above
  # multiple providers are tried in order (provider-level fallback after key-level fallback exhausted)
  "gpt-4o":
    - my-openai
    - my-oai
  "deepseek-chat":
    - my-openai
  "claude-opus-5":
    - my-claude
```

**Field reference**

| Field | Required | Description |
|---|---|---|
| `server.listen` | yes | TCP address, e.g. `:8787` or `127.0.0.1:8787` |
| `server.auth_key` | yes | Gateway secret — clients pass as `Authorization: Bearer <key>` |
| `admin.listen` | no | Optional web admin panel address, e.g. `:4001`. Panel not started when empty |
| `providers[].name` | yes | Unique label used in `routes` |
| `providers[].base_url` | yes | Upstream API root (no trailing slash) |
| `providers[].type` | yes | `openai` or `anthropic` |
| `providers[].keys` | yes | One or more API keys — first key is primary, rest are fallback |
| `routes` | yes | Map of model name → list of provider names (ordered fallback) |

### Web admin panel

Set `admin.listen` in `config.yaml` to enable a browser-based config editor on its
own port (e.g. `:4001`). Open `http://your-host:4001` and log in with the same
`server.auth_key` used by clients.

```yaml
admin:
  listen: ":4001"
```

The panel lets you:

- edit gateway settings, providers (name / base_url / type / key list), and the
  model → provider routing table, or edit the raw YAML directly;
- reorder the provider fallback order in a route with ↑/↓ (first = primary);
- show/hide API keys (masked by default), and fetch the model list straight
  from a provider's own API (`/models`) with one click;
- test a provider or a route with one click (sends a tiny chat request through
  the real routing);
- save changes: they are written back to `config.yaml` atomically, and the
  gateway hot-reloads within ~1 second — no restart needed.

Notes: the admin port is plain HTTP like the gateway — bind it to `127.0.0.1:4001`
or protect it with a firewall if you expose the host. Changing `admin.listen`
itself requires a restart. Saving via the structured editor rewrites the file to
canonical YAML, so comments are dropped (the raw tab keeps them until you save).

### Usage

```bash
aiapiport start                              # start in background
aiapiport start -config /path/config.yaml   # specify config file
aiapiport status                             # check status
aiapiport logs                               # last 50 lines
aiapiport logs -n 100                        # last N lines
aiapiport logs -f                            # tail -f
aiapiport stop                               # stop
aiapiport serve -config config.yaml          # foreground (for systemd/Docker)
```

Logs and PID file default to `~/.aiapiport/`. Override with `AIAPIPORT_RUN_DIR`.
Logs are truncated at 20 MB; no backup is kept. The gateway checks `config.yaml`
once per second: valid changes to routes, providers, keys, and `auth_key` apply
to new requests automatically. Changes to `server.listen` still require a restart.

### Example request

```bash
curl http://localhost:8787/v1/chat/completions \
  -H "Authorization: Bearer sk-your-gateway-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

### Connecting clients

The gateway exposes a single OpenAI-compatible endpoint. The **base URL always ends in `/v1`** — omitting it causes a `404 page not found` error.

| Client | base_url setting |
|---|---|
| OpenAI Python/JS SDK | `http://your-host:8787/v1` |
| OpenAI-compatible apps (ChatBox, NextChat, LobeChat…) | `http://your-host:8787/v1` |
| curl / HTTP clients | full path: `http://your-host:8787/v1/chat/completions` |
| LiteLLM proxy chaining | `http://your-host:8787/v1` |
| Cursor / Copilot-style editors | `http://your-host:8787/v1` |

**Python SDK example:**

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8787/v1",
    api_key="sk-your-gateway-key",
)
response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "hi"}],
)
```

### Build from source

```bash
git clone https://github.com/mgl666/aiapiport.git
cd aiapiport
go build -ldflags "-s -w" -trimpath -o aiapiport .
```

### VPS deploy (systemd)

```bash
# cross-compile on Mac
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -trimpath -o aiapiport .

scp aiapiport root@vps:/usr/local/bin/aiapiport-bin
scp config.yaml root@vps:/etc/aiapiport/config.yaml
scp litellm-go.service root@vps:/etc/systemd/system/aiapiport.service

ssh root@vps "systemctl daemon-reload && systemctl enable --now aiapiport"
```

### Feature scope

| Feature | Status |
|---|---|
| OpenAI-compatible upstreams (DeepSeek, Moonshot, SiliconFlow…) | ✅ |
| Anthropic Claude (direct) | ✅ |
| Claude via relay (OpenAI format) | ✅ |
| SSE streaming | ✅ |
| Primary/fallback key rotation (402/429/5xx/401/403) | ✅ |
| Multi-provider fallback (model → multiple providers) | ✅ |
| Gateway auth key | ✅ |
| model → provider routing | ✅ |
| Admin UI (web config editor) | ✅ |
| Virtual keys / usage stats | ✗ (use LiteLLM) |

---

## 中文

用 Go 编写的最小化 LLM 网关，将多个上游 API 聚合为单一的 OpenAI 兼容端点。空闲内存 **~12 MB**（Python LiteLLM 约 200 MB）。

### 安装

```bash
curl -fsSL https://raw.githubusercontent.com/mgl666/aiapiport/main/install.sh | sh
```

或直接从 [Releases](https://github.com/mgl666/aiapiport/releases) 下载对应平台的二进制文件。

### 配置

**第一步 — 创建配置文件**

默认情况下 `aiapiport` 会在**当前工作目录**查找 `config.yaml`。也可以放在任意位置，用 `-config` 指定：

```bash
aiapiport start -config /etc/aiapiport/config.yaml
```

**第二步 — 填写配置**

复制示例文件后编辑：

```bash
cp config.yaml.example config.yaml
```

```yaml
server:
  listen: ":8787"          # 监听地址和端口
  auth_key: "sk-change-me" # 客户端必须携带此 key 作为 Bearer token

providers:
  # OpenAI 兼容上游（OpenAI、DeepSeek、SiliconFlow、中转站等）
  - name: my-openai         # 任意名称，在 routes 中引用
    base_url: "https://api.openai.com/v1"
    type: openai            # openai 兼容 API 填 "openai"
    keys:
      - "sk-主key"
      - "sk-备用key"        # 可选，429/5xx/401 时自动切换

  # Anthropic Claude 直连
  - name: my-claude
    base_url: "https://api.anthropic.com"
    type: anthropic         # Claude 直连 API 填 "anthropic"
    keys:
      - "sk-ant-xxxxxxxxxx"

routes:
  # 客户端请求的 model 名 → 上面定义的 provider 名（可多个，按顺序 fallback）
  "gpt-4o":
    - my-openai
  "deepseek-chat":
    - my-openai
  "claude-opus-5":
    - my-claude
```

**字段说明**

| 字段 | 必填 | 说明 |
|---|---|---|
| `server.listen` | 是 | 监听地址，如 `:8787` 或 `127.0.0.1:8787` |
| `server.auth_key` | 是 | 网关密钥，客户端用 `Authorization: Bearer <key>` 传入 |
| `admin.listen` | 否 | 可选网页管理面板监听地址，如 `:4001`；留空则不启动 |
| `providers[].name` | 是 | provider 唯一标识，在 `routes` 中引用 |
| `providers[].base_url` | 是 | 上游 API 根地址（不含末尾斜杠） |
| `providers[].type` | 是 | `openai` 或 `anthropic` |
| `providers[].keys` | 是 | API key 列表，第一个为主 key，其余为备用 |
| `routes` | 是 | model 名 → provider 名列表（按顺序 fallback） |

### 网页管理面板

在 `config.yaml` 里配置 `admin.listen` 即可启用网页版配置编辑器（独立端口，如 `:4001`）。
浏览器打开 `http://你的服务器:4001`，用和客户端相同的 `server.auth_key` 登录。

```yaml
admin:
  listen: ":4001"
```

面板支持：

- 编辑网关设置、服务商（名称 / base_url / 类型 / key 列表）和 model → provider
  路由表，也可以直接编辑原始 YAML；
- 路由中可用 ↑/↓ 直接调整服务商顺序（第一个为主，其余按序 fallback）；
- API key 默认打码显示，可单个或全部切换显示/隐藏；
- 一键从服务商自己的 API（`/models`）拉取可用模型列表，选中即填入测试框；
- 一键测试某个服务商或路由（通过真实路由发一条极小的 chat 请求）；
- 保存：原子写回 `config.yaml`，网关约 1 秒内热重载生效，**无需重启**。

注意：管理端口和网关一样是明文 HTTP——若机器暴露在外网，建议绑 `127.0.0.1:4001`
或用防火墙保护。修改 `admin.listen` 本身需要重启。通过结构化表单保存会把文件重写为
规范化 YAML（注释会丢失）；原始配置页在你保存前会一直保留手写注释。

### 使用

```bash
aiapiport start                              # 后台启动
aiapiport start -config /path/config.yaml   # 指定配置文件
aiapiport status                             # 查看状态
aiapiport logs                               # 最后 50 行日志
aiapiport logs -n 100                        # 最后 N 行
aiapiport logs -f                            # 实时跟踪
aiapiport stop                               # 停止
aiapiport serve -config config.yaml          # 前台运行（适合 systemd/Docker）
```

日志和 PID 文件默认在 `~/.aiapiport/`，可用 `AIAPIPORT_RUN_DIR` 覆盖。
日志到 20 MB 时会截断，不保留备份。网关每秒检查一次 `config.yaml`：
路由、provider、key 和 `auth_key` 的有效修改会自动应用到新请求；修改
`server.listen` 仍需重启。

### 调用示例

```bash
curl http://localhost:8787/v1/chat/completions \
  -H "Authorization: Bearer sk-你的网关key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

### 客户端接入

网关暴露标准 OpenAI 兼容接口，**base URL 必须以 `/v1` 结尾**，否则会返回 `404 page not found`。

| 客户端 | base_url 填写 |
|---|---|
| OpenAI Python/JS SDK | `http://你的服务器:8787/v1` |
| ChatBox、NextChat、LobeChat 等应用 | `http://你的服务器:8787/v1` |
| curl / HTTP 直接调用 | 完整路径：`http://你的服务器:8787/v1/chat/completions` |
| LiteLLM 上级代理 | `http://你的服务器:8787/v1` |
| Cursor / 兼容 OpenAI 的编辑器 | `http://你的服务器:8787/v1` |

**Python SDK 示例：**

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8787/v1",
    api_key="sk-你的网关key",
)
response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "hi"}],
)
```

### 从源码构建

```bash
git clone https://github.com/mgl666/aiapiport.git
cd aiapiport
go build -ldflags "-s -w" -trimpath -o aiapiport .
```

### 功能范围

| 功能 | 支持 |
|---|---|
| OpenAI 及兼容（DeepSeek/Moonshot/SiliconFlow...） | ✅ |
| Anthropic Claude（直连） | ✅ |
| 经 relay 中转的 Claude（OpenAI 格式） | ✅ |
| SSE 流式输出 | ✅ |
| 主备 key fallback（402/429/5xx/401/403 自动切换） | ✅ |
| 多 provider fallback（一个 model → 多个 provider） | ✅ |
| 网关固定 key 鉴权 | ✅ |
| model → provider 路由 | ✅ |
| Admin UI（网页配置编辑器） | ✅ |
| 虚拟 key / 用量统计 | ✗（请用 LiteLLM） |
