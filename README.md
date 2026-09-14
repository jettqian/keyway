# Keyway

自托管、多租户、纯转发的 AI API 网关（BYOK）。Agent 只配置一次地址与令牌；
供应商切换、线路优选、密钥轮换全部在服务端完成、即时生效。

## 文档

- [需求文档（PRD）](docs/PRD.md)
- [技术方案（DESIGN）](docs/DESIGN.md)

## 快速开始

```bash
# 生成主密钥并写入环境
export KEYWAY_SECRET=$(openssl rand -base64 32)

docker compose up -d          # 默认 0.0.0.0:8080，SQLite 落在 ./data
# 首个注册用户自动成为管理员
```

本地开发（不依赖 Docker）：

```bash
cd server && go run ./cmd/keyway      # 后端 :8080
cd web && npm run dev                 # 前端 :5173（/api 代理到 8080）
```

Agent 侧一次性配置（之后零改动）：

```bash
# OpenAI 兼容客户端
export OPENAI_BASE_URL=https://your-domain/v1
export OPENAI_API_KEY=sk-keyway-...

# Claude Code
export ANTHROPIC_BASE_URL=https://your-domain
export ANTHROPIC_AUTH_TOKEN=sk-keyway-...
```

## 核心特性（规划）

- 统一 OpenAI / Anthropic 兼容端点，Claude Code / Cline 直连
- 多租户 BYOK：上游密钥独立成池，渠道自由组合（≤5 key/渠道），429 自动轮换
- 多线路（≤5 base_url）+ 线路×路径探测优选，故障自动降级
- 出站代理：个人代理 + 管理员公共代理池（按渠道 opt-in，流量统计）
- 预制渠道模板：管理员沉淀线路/模型知识，用户一键复制 + 贴 key
- 用量/费用统计（价目表估算），按用户/模型/渠道/密钥聚合
- 飞书登录、开放注册（可邀请码制）

状态：MVP 功能全部落地（后端 + 控制台前端），飞书 OAuth 与部分管理界面表单在收尾中，见 docs/PRD.md 里程碑。
