# CLIProxyAPI 认证与 Codex 优化版

[English](README.md) | 中文

本仓库是基于 `router-for-me/CLIProxyAPI` 修改而来的独立维护分支。

它不是上游官方仓库，与 `router-for-me` 没有关联，不应被描述为上游的官方镜像、发布渠道、支持渠道或文档入口。

## 这个分支的定位

这个分支保留了原项目面向 CLI 代理兼容层的核心目标，但当前维护重点更偏向运行时行为调整，而不是商业推广内容。

相对于 `router-for-me/CLIProxyAPI`，当前分支的主要改动集中在：

- Codex / OpenAI Responses 请求翻译与执行器接线调整
- 认证调度器在高并发下的调度与抖动控制优化
- 异步认证持久化补充
- 调度器基准测试与持久化测试更新
- 容器默认镜像改为当前仓库自己的 GHCR 发布地址

目前 `go.mod` 仍保留 `github.com/router-for-me/CLIProxyAPI/v6` 模块路径以兼容现有代码结构。这个兼容性安排不代表与上游存在项目关联。

## 核心能力

- 面向 CLI 客户端的 OpenAI、Gemini、Claude、Codex 兼容 API
- 支持 Codex、Claude Code、Qwen Code、iFlow 的 OAuth 接入
- 支持流式与非流式响应
- 支持多账户路由与负载均衡
- 提供可复用的 Go SDK：`sdk/cliproxy`
- 适合二次嵌入的请求翻译与 Provider 执行层

## 快速开始

使用本分支的 GHCR 镜像：

```bash
docker run --rm -p 8317:8317 ghcr.io/arron196/cliproxyapi:latest
```

或者直接使用 Compose：

```bash
docker compose up -d
```

`docker-compose.yml` 当前默认镜像为：

```text
ghcr.io/arron196/cliproxyapi:latest
```

## 本地文档

- SDK 使用文档：[docs/sdk-usage_CN.md](docs/sdk-usage_CN.md)
- SDK 高级主题：[docs/sdk-advanced_CN.md](docs/sdk-advanced_CN.md)
- SDK 访问认证：[docs/sdk-access_CN.md](docs/sdk-access_CN.md)
- SDK Watcher 集成：[docs/sdk-watcher_CN.md](docs/sdk-watcher_CN.md)

开启相关配置后，管理端点会暴露在 `/v0/management`。本分支当前不提供独立的外部文档站点。

## ChatGPT Web access_token 模式

当前分支额外支持把 `ChatGPT Web access_token` 作为 Codex 上游凭证使用：

- 对外入口仍然是 `POST /v1/responses`
  - 代理会在内部按需在 `chatgpt.com/backend-api/codex/responses`
    和 `chatgpt.com/backend-api/conversation` 之间自动切换
  - 更适合只有 ChatGPT Web `access_token` / `account_id` 的场景

如果你想直接导入 auth 文件，可使用类似下面的最小结构：

```json
{
  "type": "codex",
  "email": "you@example.com",
  "access_token": "eyJ...",
  "account_id": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "base_url": "https://chatgpt.com"
}
```

兼容的账号字段别名还包括：

- `workspace_id`
- `chatgpt_account_id`

如果只有 `access_token`，没有 `refresh_token`，则不会自动刷新；过期后需要重新登录并重新导入新的 token。

### chat2api sidecar 回退

当前分支还支持在 `refresh_token` 缺失时，自动把 ChatGPT/Codex 请求转发到本地 `chat2api` sidecar。

- 配置项：`chat2api-url`
- Compose 联动示例：见 `docker-compose.yml`
- 推荐容器内地址：`http://chat2api:5005`

快速验证可直接执行：

```bash
bash examples/test_chat_model.sh http://127.0.0.1:8317 123456 gpt-5.4 "Hello"
```

> 说明：当前仓库本体不包含管理面板前端源码，所以“按钮类”联调入口不在这里；这里只提供 API/Compose/脚本侧联调能力。

## 项目身份说明

- 上游基线：`router-for-me/CLIProxyAPI`
- 当前仓库：独立维护的衍生分支
- 与上游关系：无关联、无背书、无共享发布流程、无共享支持义务

如果你需要上游默认行为或上游支持，请直接使用上游仓库。

## 贡献

欢迎围绕当前仓库的行为和文档提交修改，但请不要把上游仓库的发布承诺或商业集成视为本仓库的一部分。

## 许可证

MIT，见 [LICENSE](LICENSE)。
