import React from 'react'
import { Alert, Button, Card, Steps, Tabs, Typography, message } from 'antd'
import { CopyOutlined } from '@ant-design/icons'

const TOKEN_PLACEHOLDER = 'sk-keyway-你的令牌'

const CodeBlock: React.FC<{ text: string }> = ({ text }) => {
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      message.success('已复制')
    } catch {
      message.error('复制失败，请手动选择复制')
    }
  }
  return (
    <div style={{ position: 'relative', background: '#173042', borderRadius: 8, marginBottom: 12 }}>
      <pre style={{ margin: 0, padding: '12px 64px 12px 16px', color: '#dce9f0', fontSize: 12.5, lineHeight: 1.7, overflowX: 'auto' }}>
        {text}
      </pre>
      <Button size="small" icon={<CopyOutlined />} onClick={copy} style={{ position: 'absolute', top: 8, right: 8 }} />
    </div>
  )
}

const Note: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <Typography.Paragraph type="secondary" style={{ fontSize: 13, marginTop: 4 }}>
    {children}
  </Typography.Paragraph>
)

const GuidePage: React.FC = () => {
  const origin = window.location.origin

  return (
    <div>
      <div className="page-heading">
        <div><h2>接入指南</h2><p>把 Claude Code、Codex、opencode 等客户端指向本网关，只需配置一次。</p></div>
      </div>

      <Card style={{ marginBottom: 16 }}>
        <Steps
          direction="vertical"
          size="small"
          current={-1}
          items={[
            { title: '准备渠道', description: <>在<a href="#/channels">渠道</a>页创建或从<a href="#/templates">预制模板</a>复制渠道，绑定密钥并启用；模型列表从模型目录点选。</> },
            { title: '创建网关令牌', description: <>在<a href="#/tokens">令牌</a>页新建并复制 <Typography.Text code>sk-keyway-…</Typography.Text>，下游客户端只用这个令牌。</> },
            { title: '配置客户端', description: '按下方对应客户端的说明配置；网关地址用本页显示的地址（生产环境替换为你的域名）。' },
          ]}
        />
        <Alert
          style={{ marginTop: 4 }}
          type="info"
          showIcon
          message={<>当前网关地址：<Typography.Text code copyable>{origin}</Typography.Text></>}
          description="客户端里填写的模型名 = 渠道中配置的模型名，可在「模型管理」页查看；透明转发下 OpenAI 与 Anthropic 协议客户端可共用同一令牌。"
        />
      </Card>

      <Card>
        <Tabs
          items={[
            {
              key: 'claude',
              label: 'Claude Code',
              children: (
                <div>
                  <Typography.Title level={5} style={{ marginTop: 0 }}>方式一：环境变量（推荐）</Typography.Title>
                  <CodeBlock text={`export ANTHROPIC_BASE_URL=${origin}\nexport ANTHROPIC_AUTH_TOKEN=${TOKEN_PLACEHOLDER}\n\n# 然后直接运行\nclaude`} />
                  <Note>BASE_URL 不带 <Typography.Text code>/v1</Typography.Text>，客户端会自动拼接 <Typography.Text code>/v1/messages</Typography.Text>。</Note>
                  <Typography.Title level={5}>方式二：写入配置文件</Typography.Title>
                  <CodeBlock text={`// ~/.claude/settings.json\n{\n  "env": {\n    "ANTHROPIC_BASE_URL": "${origin}",\n    "ANTHROPIC_AUTH_TOKEN": "${TOKEN_PLACEHOLDER}"\n  }\n}`} />
                  <Note>保存后重启 Claude Code 生效；模型名填渠道中配置的名称（如 <Typography.Text code>claude-sonnet-4.5</Typography.Text>）。</Note>
                </div>
              ),
            },
            {
              key: 'codex',
              label: 'Codex',
              children: (
                <div>
                  <Typography.Title level={5} style={{ marginTop: 0 }}>配置文件 ~/.codex/config.toml</Typography.Title>
                  <CodeBlock text={`model = "gpt-5.2"                 # 改成渠道中配置的模型名\nmodel_provider = "keyway"\n\n[model_providers.keyway]\nname = "keyway"\nbase_url = "${origin}/v1"\nwire_api = "chat"                # 网关走 chat/completions，必须为 chat\nenv_key = "KEYWAY_API_KEY"`} />
                  <Typography.Title level={5}>设置密钥并启动</Typography.Title>
                  <CodeBlock text={`export KEYWAY_API_KEY=${TOKEN_PLACEHOLDER}\n\n# 然后直接运行\ncodex`} />
                  <Note><Typography.Text code>wire_api</Typography.Text> 必须为 <Typography.Text code>chat</Typography.Text>（网关暂未实现 Responses API）；也可以把密钥写入环境变量配置（如 shell profile）长期生效。</Note>
                </div>
              ),
            },
            {
              key: 'opencode',
              label: 'opencode',
              children: (
                <div>
                  <Typography.Title level={5} style={{ marginTop: 0 }}>配置文件 opencode.json（项目根目录或 ~/.config/opencode/）</Typography.Title>
                  <CodeBlock text={`{\n  "$schema": "https://opencode.ai/config.json",\n  "provider": {\n    "keyway": {\n      "npm": "@ai-sdk/openai-compatible",\n      "name": "Keyway",\n      "options": {\n        "baseURL": "${origin}/v1",\n        "apiKey": "{env:KEYWAY_API_KEY}"\n      },\n      "models": {\n        "gpt-5.2": {},\n        "claude-sonnet-4.5": {}\n      }\n    }\n  },\n  "model": "keyway/gpt-5.2"\n}`} />
                  <Typography.Title level={5}>设置密钥并启动</Typography.Title>
                  <CodeBlock text={`export KEYWAY_API_KEY=${TOKEN_PLACEHOLDER}\n\n# 然后直接运行\nopencode`} />
                  <Note>
                    <Typography.Text code>models</Typography.Text> 中列出渠道实际配置的模型名；也可改用
                    <Typography.Text code>@ai-sdk/anthropic</Typography.Text>（<Typography.Text code>baseURL</Typography.Text> 同样填
                    <Typography.Text code>{origin}/v1</Typography.Text>）走 Anthropic 协议。
                  </Note>
                </div>
              ),
            },
            {
              key: 'generic',
              label: '通用 OpenAI 兼容',
              children: (
                <div>
                  <Typography.Title level={5} style={{ marginTop: 0 }}>任意支持 OpenAI 协议的客户端 / SDK（Cline、Roo 等）</Typography.Title>
                  <CodeBlock text={`Base URL: ${origin}/v1\nAPI Key:  ${TOKEN_PLACEHOLDER}`} />
                  <Typography.Title level={5}>连通性验证</Typography.Title>
                  <CodeBlock text={`curl ${origin}/v1/chat/completions \\\n  -H "Authorization: Bearer ${TOKEN_PLACEHOLDER}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"gpt-5.2","messages":[{"role":"user","content":"hi"}]}'`} />
                  <Note>可用 <Typography.Text code>GET {origin}/v1/models</Typography.Text> 查看当前令牌可路由的全部模型。</Note>
                </div>
              ),
            },
          ]}
        />
      </Card>
    </div>
  )
}

export default GuidePage
