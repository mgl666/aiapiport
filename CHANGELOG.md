# Changelog

All notable changes to this project will be documented in this file.

## [v0.1.4] - 2026-09-13

### Added

- **CORS 支持**：所有端点都会返回 `Access-Control-Allow-*` 响应头，并正确处理
  `OPTIONS` 预检请求。此前预检返回 405 且没有任何 CORS 头，浏览器里的网页版客户端
  （NextChat 网页版、LobeChat、Open WebUI 等）跨域调用会被浏览器直接拦截，而桌面
  客户端不受影响——这正是"有些软件能用、有些软件用不了"的主因。
- **新增 `POST /v1/completions`**：老式文本补全接口。请求会先转换成 chat 请求发给
  上游，返回结果再转换回 `text_completion` 格式（含流式逐块转换、`usage` 和
  `finish_reason`），因此只支持 chat 的上游（DeepSeek、各类中转站）也能服务老客户端。
- **新增 `POST /v1/responses`、`POST /v1/embeddings`**：按 model → provider 路由原样
  转发到上游同名路径，复用现有的 key / provider fallback。
- **新增 `GET /v1/models/{id}`**：单模型查询（`/v1/models/` 带斜杠按列表处理）。
- **鉴权方式放宽**：除 `Authorization: Bearer <key>` 外，现在也接受
  `Authorization: <key>`、`x-api-key`、`api-key` 和 `?key=` / `?api_key=` 查询参数。
- **新增 `scripts/vps-update.sh`**：VPS 手动更新脚本，从 GitHub Release 下载对应平台
  二进制，备份旧版本、原子替换、重启服务并做健康检查，启动或检查失败时自动回滚。
- **新增 `scripts/commit-and-push.sh`**：提交前自动执行 gofmt / `go vet` / 构建检查，
  并拦截 `config.yaml`、`.env`、`*.pem` 等含密钥的文件。

### Changed

- **错误响应统一为 OpenAI 风格 JSON**：401 / 404 / 405 等不再返回纯文本
  （`unauthorized`、Go 默认的 `404 page not found`），改为
  `{"error":{"message":…,"type":…,"code":…,"param":…}}`，并附带可用端点、可用模型名等
  提示，避免客户端只显示"未知错误"。
- 请求的 model 不在 `routes` 中时，错误信息会提示可用 `GET /v1/models` 查看可用模型名。
- 与上游类型不匹配的接口（例如 Claude 直连 provider 上的 `/embeddings`）会被跳过并
  返回 501 `endpoint_not_supported`，不再浪费一次 key 尝试。

### Fixed

- 修正 `daemon_unix.go` 的 gofmt 格式问题。

## [v0.1.3] - 2026-08-16

### Added

- **管理面板交互增强**：路由页支持用 ↑/↓ 直接调整服务商 fallback 顺序；
  服务商页的 API key 默认打码显示，可单个/全部切换显示与隐藏；新增"获取模型"
  按钮，可从服务商自己的 API（openai 走 `{base_url}/models`、anthropic 走
  `{base_url}/v1/models`）拉取可用模型列表并一键填入测试框。

## [v0.1.2] - 2026-08-16

### Added

- **网页管理面板（Admin UI）**：新增 `admin.listen` 配置项，可启动独立端口的
  网页版配置编辑器。浏览器打开后使用 `server.auth_key` 登录，即可在界面中
  增删改服务商、编辑 model → provider 路由、直接编辑原始 YAML，并一键测试
  服务商/路由连通性。保存后配置原子写回 `config.yaml`，网关约 1 秒内热重载
  生效，无需重启。管理面板内嵌于二进制（`go:embed`），无外部依赖。

## [v0.1.1] - 2026-07-28

### Fixed

- 修复 `gateway` 包中缺失的 `router` 导入，解决构建失败的问题。

## [v0.1.0] - 2026-07-28

### Added

- **核心功能**：轻量级 LLM API 网关，将多个上游 API 聚合为单一的 OpenAI 兼容端点。
- **多 Provider 支持**：支持 OpenAI 兼容上游（DeepSeek、Moonshot、SiliconFlow、OneAPI 等）和 Anthropic Claude 原生 API。
- **双向格式转换**：Anthropic 请求/响应自动与 OpenAI 格式互转，包括非流式和 SSE 流式。
- **智能 Fallback**：支持 Key 级主备切换（402/429/5xx/401/403/网络错误自动重试）和 Provider 级多级降级。
- **SSE 流式传输**：完整支持 Server-Sent Events 流式响应，流式开始前自动 fallback。
- **模型路由**：通过 `config.yaml` 灵活配置 model → provider 映射，支持一对多路由。
- **网关鉴权**：基于 Bearer Token 的请求认证。
- **热重载配置**：每秒检测配置文件变化，自动应用路由、Provider、Key 和 auth_key 的更新，无需重启。
- **Daemon 模式**：提供 `start/stop/status/logs` 命令行管理，支持后台运行和日志查看。
- **跨平台编译**：支持 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64。
- **一键安装脚本**：`install.sh` 自动检测平台并下载安装对应二进制文件。
- **systemd 服务**：提供 `aiapiport.service` 文件，方便 Linux VPS 部署。
- **极低资源占用**：空闲内存仅 ~12 MB（对比 Python LiteLLM 约 200 MB）。
- **日志轮转**：自动限制日志文件大小在 20 MB 以内。
- **健康检查**：`GET /health` 端点用于监控探活。
- **模型列表**：`GET /v1/models` 返回 OpenAI 格式的模型列表。

[Unreleased]: https://github.com/mgl666/aiapiport/compare/v0.1.4...HEAD
[v0.1.4]: https://github.com/mgl666/aiapiport/compare/v0.1.3...v0.1.4
[v0.1.3]: https://github.com/mgl666/aiapiport/compare/v0.1.2...v0.1.3
[v0.1.2]: https://github.com/mgl666/aiapiport/compare/v0.1.1...v0.1.2
[v0.1.1]: https://github.com/mgl666/aiapiport/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/mgl666/aiapiport/releases/tag/v0.1.0