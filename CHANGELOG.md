# Changelog

All notable changes to this project will be documented in this file.

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

[Unreleased]: https://github.com/mgl666/aiapiport/compare/v0.1.2...HEAD
[v0.1.2]: https://github.com/mgl666/aiapiport/compare/v0.1.1...v0.1.2
[v0.1.1]: https://github.com/mgl666/aiapiport/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/mgl666/aiapiport/releases/tag/v0.1.0