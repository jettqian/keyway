# Keyway

自托管、多租户、纯转发的 AI API 网关（BYOK）。Agent 只配置一次地址与令牌；
供应商切换、线路优选、密钥轮换全部在服务端完成、即时生效。

## 文档

- [需求文档（PRD）](docs/PRD.md)
- [技术方案（DESIGN）](docs/DESIGN.md)

## 规划中的快速开始

```bash
mkdir -p data && openssl rand -base64 32   # 生成 KEYWAY_SECRET
docker compose up -d                        # 默认 0.0.0.0:8080，SQLite 落在 ./data
```

## 核心特性（规划）

- 统一 OpenAI / Anthropic 兼容端点，Claude Code / Cline 直连
- 多租户 BYOK：上游密钥独立成池，渠道自由组合（≤5 key/渠道），429 自动轮换
- 多线路（≤5 base_url）+ 线路×路径探测优选，故障自动降级
- 出站代理：个人代理 + 管理员公共代理池（按渠道 opt-in，流量统计）
- 预制渠道模板：管理员沉淀线路/模型知识，用户一键复制 + 贴 key
- 用量/费用统计（价目表估算），按用户/模型/渠道/密钥聚合
- 飞书登录、开放注册（可邀请码制）

状态：设计阶段，见 docs/。
