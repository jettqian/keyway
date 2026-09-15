# Keyway

[![License](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)](server/go.mod)
[![React](https://img.shields.io/badge/React-18-61DAFB?logo=react&logoColor=white)](web/package.json)

自托管、多租户、纯转发的 AI API 网关（BYOK）。Agent 只配置一次地址与令牌；
供应商切换、线路优选、密钥轮换全部在服务端完成、即时生效。

## 核心特性

- **统一端点**：OpenAI / Anthropic 兼容协议（含 OpenAI Responses API），Claude Code /
  Codex / opencode / Cline 直连
- **多租户 BYOK**：上游密钥独立成池，渠道自由组合（≤5 key/渠道），429 自动轮换
- **多线路优选**：每渠道 ≤5 base_url，线路×路径探测优选，故障自动降级
- **出站代理**：个人代理 + 管理员公共代理池（按渠道 opt-in，流量统计）
- **渠道模板**：管理员沉淀线路/模型知识，用户一键复制草稿 + 贴 key
- **网关令牌**：多渠道绑定、模型范围、过期与吊销即时生效
- **用量统计**：官方价目快照（含缓存档）、渠道计价模式（美元折扣 / 人民币换算比，
  汇率支持定时自动同步或固定值）、
  首字节/总耗时、按用户/模型/渠道/密钥聚合、CSV 导出
- **账号体系**：飞书登录、开放注册（可邀请码制）、内置 2026-09 官方价目（35+ 模型）

## 技术栈

| 层 | 技术 |
|---|---|
| 后端 | Go 1.22+，仅标准库 + SQLite（单二进制，无 CGO） |
| 前端 | React 18 + TypeScript + Vite，构建产物嵌入二进制 |
| 存储 | 单文件 SQLite（`./data/keyway.db`），自动迁移 |

## 快速开始（Docker）

```bash
# 生成主密钥并写入环境
export KEYWAY_SECRET=$(openssl rand -base64 32)

docker compose up -d          # 默认 0.0.0.0:20170，SQLite 落在 ./data
# 首个注册用户自动成为管理员
```

说明：
- 容器默认以 root 运行以保证挂载卷开箱可写；加固部署可 `chown 65532:65532 ./data`
  后在 compose 中加 `user: "nonroot"`
- 备份 = 停机后拷贝 `./data/keyway.db`；升级 = 换镜像 tag 重建（自动迁移表结构）

### 环境变量

| 变量 | 必填 | 说明 |
|---|---|---|
| `KEYWAY_SECRET` | ✅ | 32 字节主密钥，`openssl rand -base64 32` 生成 |
| `KEYWAY_BASE_URL` | — | 对外地址（OAuth 回调/链接展示），如 `https://keyway.example.com` |
| `KEYWAY_PROBE_INTERVAL_MIN` | — | 线路探测周期（分钟），默认 10 |
| `KEYWAY_RESPONSE_HEADER_TIMEOUT_S` | — | 上游响应头等待超时（秒），默认 1800，0=不限制；仅覆盖响应头阶段，流式响应不受影响 |
| `KEYWAY_IDLE_STREAM_TIMEOUT_S` | — | 流式空闲超时（秒），默认 300，0=关闭；流式期间每 15s 发 SSE 注释 ping 保活，上游持续无数据则关闭上游止损 |
| `KEYWAY_LOG_RETENTION_DAYS` | — | 请求日志保留期（天），默认 30 |
| `KEYWAY_FX_SOURCE_URL` | — | 覆盖汇率同步源（默认 frankfurter → jsdelivr → er-api 三源回退，每 24h；管理员亦可在设置页选固定值） |

## 本地开发

```bash
cd server && go run ./cmd/keyway      # 后端 :8080
cd web && npm run dev                 # 前端 :5173（/api 代理到 8080）
```

## Agent 接入

Agent 侧一次性配置（之后零改动；控制台「接入指南」页自动填充本机地址并支持一键复制）。
网关地址带不带 `/v1` 均可（`/v1/chat/completions` 与 `/chat/completions`、`/v1/messages`
与 `/messages` 等价），按客户端习惯填写：

```jsonc
// Claude Code：~/.claude/settings.json（推荐直接写密钥）
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://your-domain",
    "ANTHROPIC_AUTH_TOKEN": "sk-keyway-..."
  }
}
```

```toml
# Codex：~/.codex/config.toml（Responses 与 chat 协议均可；密钥直接写入）
model = "gpt-5.2"                 # 渠道中配置的模型名
model_provider = "keyway"

[model_providers.keyway]
name = "keyway"
base_url = "https://your-domain/v1"
wire_api = "chat"                 # 或 "responses"（默认），网关均支持
experimental_bearer_token = "sk-keyway-..."
```

```jsonc
// opencode：项目根目录或 ~/.config/opencode/opencode.json
// 优先 @ai-sdk/openai / @ai-sdk/anthropic（OpenAI 与 Anthropic 协议各一个，共用令牌）
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "keyway": {
      "npm": "@ai-sdk/openai",
      "name": "Keyway",
      "options": { "baseURL": "https://your-domain/v1", "apiKey": "sk-keyway-..." },
      "models": { "gpt-5.2": {} }
    },
    "keyway-anthropic": {
      "npm": "@ai-sdk/anthropic",
      "name": "Keyway (Anthropic)",
      "options": { "baseURL": "https://your-domain/v1", "apiKey": "sk-keyway-..." },
      "models": { "claude-sonnet-4.5": {} }
    }
  },
  "model": "keyway/gpt-5.2"
}
```

```bash
# 任意 OpenAI 兼容客户端 / SDK（或临时会话用环境变量）
export OPENAI_BASE_URL=https://your-domain/v1
export OPENAI_API_KEY=sk-keyway-...
```

## 目录结构

```
├── docker-compose.yml    # 一键部署
├── Dockerfile            # 前后端一体构建
├── server/               # Go 后端
│   ├── cmd/keyway/       # 入口
│   └── internal/         # api / auth / relay / routing / probe / usage / store ...
├── web/                  # React 控制台
└── docs/                 # PRD 与技术方案
```

## 文档

- [需求文档（PRD）](docs/PRD.md)
- [技术方案（DESIGN）](docs/DESIGN.md)

## 状态

MVP 功能全部落地（后端 + 控制台前端），飞书 OAuth 与部分管理界面表单在收尾中，
见 [PRD 里程碑](docs/PRD.md)。

## 许可证

[MIT](./LICENSE) © 2026 jettqian
