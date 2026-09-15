import React from 'react'
import { Alert, Button, Card, Checkbox, Col, Collapse, Row, Select, Steps, Tabs, Typography, message } from 'antd'
import { CopyOutlined, RobotOutlined } from '@ant-design/icons'
import { listTokens, revealToken } from '../api'
import type { GatewayToken } from '../api/types'
import { copyText } from '../copy'

const TOKEN_PLACEHOLDER = 'sk-keyway-你的令牌'
const MASKED_TOKEN = 'sk-keyway-••••••••（复制时自动替换为真实令牌）'

const CLIENTS = [
  { key: 'claude', label: 'Claude Code' },
  { key: 'codex', label: 'Codex' },
  { key: 'opencode', label: 'opencode' },
] as const
type ClientKey = (typeof CLIENTS)[number]['key']

const CodeBlock: React.FC<{ text: string }> = ({ text }) => {
  const copy = async () => {
    if (await copyText(text)) {
      message.success('已复制')
    } else {
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

// 行内渲染：`代码` 与 **加粗**
const renderInline = (text: string, keyBase: string): React.ReactNode[] => {
  const parts: React.ReactNode[] = []
  const regex = /`([^`]+)`|\*\*([^*]+)\*\*/g
  let last = 0
  let m: RegExpExecArray | null
  let i = 0
  while ((m = regex.exec(text)) !== null) {
    if (m.index > last) parts.push(text.slice(last, m.index))
    if (m[1] !== undefined) {
      parts.push(<Typography.Text code key={`${keyBase}-c${i}`}>{m[1]}</Typography.Text>)
    } else {
      parts.push(<Typography.Text strong key={`${keyBase}-b${i}`}>{m[2]}</Typography.Text>)
    }
    last = m.index + m[0].length
    i++
  }
  if (last < text.length) parts.push(text.slice(last))
  return parts
}

// 轻量 Markdown 渲染（覆盖指令用到的语法：## 标题、- 列表、1. 列表、缩进续行）
const MdView: React.FC<{ text: string }> = ({ text }) => {
  const out: React.ReactNode[] = []
  let list: { ordered: boolean; items: React.ReactNode[][] } | null = null
  const flush = (key: string) => {
    if (!list) return
    const items = list.items.map((its, i) => <li key={i}>{its}</li>)
    out.push(
      list.ordered ? (
        <ol key={key} style={{ margin: '4px 0 8px', paddingLeft: 22 }}>{items}</ol>
      ) : (
        <ul key={key} style={{ margin: '4px 0 8px', paddingLeft: 22 }}>{items}</ul>
      ),
    )
    list = null
  }
  text.split('\n').forEach((line, idx) => {
    const key = String(idx)
    if (line.startsWith('## ')) {
      flush(`f${key}`)
      out.push(
        <Typography.Title key={key} level={5} style={{ margin: '12px 0 4px' }}>
          {renderInline(line.slice(3), key)}
        </Typography.Title>,
      )
      return
    }
    if (/^- /.test(line)) {
      if (!list || list.ordered) flush(`f${key}`)
      if (!list) list = { ordered: false, items: [] }
      list.items.push(renderInline(line.slice(2), key))
      return
    }
    const olm = /^(\d+)\. (.*)$/.exec(line)
    if (olm) {
      if (!list || !list.ordered) flush(`f${key}`)
      if (!list) list = { ordered: true, items: [] }
      list.items.push(renderInline(olm[2], key))
      return
    }
    if (line.trim() === '') {
      flush(`f${key}`)
      return
    }
    if (list && list.items.length > 0 && /^\s+/.test(line)) {
      const prev = list.items[list.items.length - 1]
      prev.push(<br key={`br${key}`} />)
      prev.push(...renderInline(line.trim(), key))
      return
    }
    flush(`f${key}`)
    out.push(
      <Typography.Paragraph key={key} style={{ margin: '4px 0' }}>
        {renderInline(line, key)}
      </Typography.Paragraph>,
    )
  })
  flush('f-end')
  return <div style={{ fontSize: 13.5 }}>{out}</div>
}

// 构建给 AI 的配置指令（含真实令牌与可用模型；仅包含所选客户端）
const buildAIPrompt = (origin: string, plaintext: string, models: string[], clients: ClientKey[]): string => {
  const modelLine = models.length
    ? models.join(', ')
    : '（在网关「模型管理」页查看，或 GET ' + origin + '/v1/models）'
  const tasks: Partial<Record<ClientKey, string>> = {
    claude: `1. Claude Code：编辑 ~/.claude/settings.json，在 env 中写入
   ANTHROPIC_BASE_URL = "${origin}"、ANTHROPIC_AUTH_TOKEN = 上面的网关令牌。`,
    codex: `2. Codex：编辑 ~/.codex/config.toml，添加自定义 provider：
   [model_providers.keyway] 使用 base_url = "${origin}/v1"、wire_api = "chat"、
   experimental_bearer_token = 网关令牌；并在顶部设置 model = 一个可用模型、model_provider = "keyway"。
   注意 wire_api 必须是 chat（网关暂未实现 Responses API）。`,
    opencode: `3. opencode：编辑 ~/.config/opencode/opencode.json（或项目根目录 opencode.json），
   添加 provider "keyway"（npm = "@ai-sdk/openai"，baseURL = "${origin}/v1"，apiKey = 网关令牌）
   和 "keyway-anthropic"（npm = "@ai-sdk/anthropic"，baseURL = "${origin}/v1"，apiKey = 网关令牌），
   models 按上面列出的可用模型填写。`,
  }
  const selected = CLIENTS.filter((c) => clients.includes(c.key))
  const taskLines = selected
    .map((c, i) => tasks[c.key]!.replace(/^\d+\./, `${i + 1}.`))
    .join('\n')
  const taskSection =
    selected.length > 0
      ? `\n## 任务（改完逐项验证）\n${taskLines}\n`
      : '\n## 任务\n（未选择客户端，仅保存以上信息备用。）\n'
  return `请帮我配置 AI 网关（Keyway）客户端。以下信息已齐全，直接使用即可：

## 网关信息
- OpenAI 兼容地址: ${origin}/v1（不带 /v1 的 ${origin} 也可以）
- Anthropic 协议地址: ${origin}（Claude Code 用，客户端自动拼接 /v1/messages）
- 网关令牌: ${plaintext}
- 可用模型: ${modelLine}
${taskSection}
注意事项：令牌是敏感信息，只写入本机配置文件，不要提交到代码仓库；改完各发一条测试消息验证连通。`
}

const AIHelpCard: React.FC<{ origin: string }> = ({ origin }) => {
  const [tokens, setTokens] = React.useState<GatewayToken[]>([])
  const [tokenId, setTokenId] = React.useState<number | undefined>()
  const [clients, setClients] = React.useState<ClientKey[]>(['claude', 'codex', 'opencode'])
  const [copying, setCopying] = React.useState(false)
  const [copyFailed, setCopyFailed] = React.useState(false)
  const [realPrompt, setRealPrompt] = React.useState<string | null>(null)
  const [previewOpen, setPreviewOpen] = React.useState<string[]>([])
  // 复制材料（真实令牌 + 可路由模型列表）：仅存 ref、不渲染。选中令牌时预取，
  // 点击复制时若已就绪即可在用户手势的同步调用栈内执行剪贴板写入，
  // 避免 await 网络请求后丢失手势（Firefox / 部分内嵌浏览器会判定剪贴板不可用）
  const materialRef = React.useRef<{ plaintext: string; models: string[] } | null>(null)

  React.useEffect(() => {
    listTokens()
      .then((r) => {
        const valid = r.tokens.filter((t) => !t.revoked)
        setTokens(valid)
        setTokenId(valid[0]?.id)
      })
      .catch(() => {})
  }, [])

  const fetchModels = async (plaintext: string): Promise<string[]> => {
    try {
      const resp = await fetch(`${origin}/v1/models`, { headers: { Authorization: `Bearer ${plaintext}` } })
      if (!resp.ok) return []
      const data = await resp.json()
      return (data?.data ?? []).map((m: { id: string }) => m.id).slice(0, 30)
    } catch {
      return []
    }
  }

  // 选中令牌后预取复制材料（令牌切换即作废重取）
  React.useEffect(() => {
    materialRef.current = null
    if (!tokenId) return
    let stale = false
    revealToken(tokenId)
      .then(async (r) => {
        const models = await fetchModels(r.plaintext)
        if (!stale) materialRef.current = { plaintext: r.plaintext, models }
      })
      .catch(() => {})
    return () => {
      stale = true
    }
  }, [tokenId])

  // 切换令牌/客户端后，清空剪贴板失败态与暂存的真实指令
  React.useEffect(() => {
    setCopyFailed(false)
    setRealPrompt(null)
  }, [tokenId, clients])

  const copyPrompt = async () => {
    if (!tokenId) return
    setCopying(true)
    try {
      let plaintext: string
      let models: string[]
      const material = materialRef.current
      if (material) {
        ;({ plaintext, models } = material)
      } else {
        const r = await revealToken(tokenId)
        plaintext = r.plaintext
        models = await fetchModels(plaintext)
      }
      const text = buildAIPrompt(origin, plaintext, models, clients)
      if (await copyText(text)) {
        message.success('已复制，粘贴给任意 AI 工具即可代为配置')
        setCopyFailed(false)
        setRealPrompt(null)
      } else {
        // 剪贴板完全不可用：自动展开预览并展示真实指令供手动复制
        setRealPrompt(text)
        setCopyFailed(true)
        setPreviewOpen(['preview'])
      }
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setCopying(false)
    }
  }

  const retryCopy = async () => {
    if (!realPrompt) return
    if (await copyText(realPrompt)) {
      message.success('已复制')
      setCopyFailed(false)
      setRealPrompt(null)
    } else {
      message.error('复制仍失败，请全选预览文本手动复制')
    }
  }

  const toggleClient = (key: ClientKey, checked: boolean) => {
    setClients((prev) => {
      const next = checked ? [...prev, key] : prev.filter((k) => k !== key)
      return next.length > 0 ? next : prev
    })
  }

  const maskedPrompt = buildAIPrompt(origin, MASKED_TOKEN, ['（复制时包含该令牌可路由的全部模型）'], clients)

  return (
    <Card style={{ marginBottom: 16 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
        <RobotOutlined style={{ fontSize: 20, color: '#176b87' }} />
        <Typography.Title level={5} style={{ margin: 0 }}>让 AI 帮你配置</Typography.Title>
        <div style={{ flex: 1 }} />
        <Select
          style={{ minWidth: 200 }}
          placeholder={tokens.length ? '选择令牌' : '暂无有效令牌'}
          value={tokenId}
          onChange={setTokenId}
          options={tokens.map((t) => ({ value: t.id, label: `${t.name}（${t.keyPrefix}…）` }))}
          notFoundContent={<span>请先到「令牌」页创建令牌</span>}
        />
        <Button type="primary" icon={<CopyOutlined />} loading={copying} disabled={!tokenId} onClick={copyPrompt}>
          复制配置指令（含令牌）
        </Button>
      </div>
      <Row style={{ marginTop: 8 }}>
        <Col>
          <span style={{ color: '#71858d', fontSize: 12, marginRight: 12 }}>配置哪些客户端：</span>
          {CLIENTS.map((c) => (
            <Checkbox
              key={c.key}
              checked={clients.includes(c.key)}
              onChange={(e) => toggleClient(c.key, e.target.checked)}
              style={{ marginRight: 16 }}
            >
              {c.label}
            </Checkbox>
          ))}
        </Col>
      </Row>
      <Note>
        指令包含网关地址、所选令牌的完整明文和可用模型列表，直接粘贴给 AI 编程工具（Claude Code / Codex / opencode
        本身或任意聊天 AI），它就能代为修改各客户端配置文件。内网环境可直接把令牌写入配置文件；若在意泄露，
        各客户端也支持环境变量方式（见下方说明）。
      </Note>
      <Collapse
        ghost
        activeKey={previewOpen}
        onChange={(keys) => setPreviewOpen(keys as string[])}
        items={[
          {
            key: 'preview',
            label: copyFailed ? '指令预览（含真实令牌，请手动复制）' : '指令预览（令牌已脱敏，复制时为真实值）',
            children: (
              <div>
                {copyFailed ? (
                  <Alert
                    type="warning"
                    showIcon
                    style={{ marginBottom: 8 }}
                    message="剪贴板不可用"
                    description={
                      <span>
                        请全选下方文本手动复制（含真实令牌，注意保密）；或
                        <a onClick={retryCopy}>重试复制</a>
                      </span>
                    }
                  />
                ) : null}
                <div style={{ background: '#f6f9fa', borderRadius: 8, padding: '8px 16px' }}>
                  <MdView text={realPrompt ?? maskedPrompt} />
                </div>
              </div>
            ),
          },
        ]}
      />
    </Card>
  )
}

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
            { title: '配置客户端', description: '直接把令牌写进客户端配置文件（内网推荐），或使用下方「让 AI 帮你配置」。' },
          ]}
        />
        <Alert
          style={{ marginTop: 4 }}
          type="info"
          showIcon
          message={<>当前网关地址：<Typography.Text code copyable>{origin}</Typography.Text></>}
          description="客户端里填写的模型名 = 渠道中配置的模型名，可在「模型管理」页查看。地址带不带 /v1 均可（/v1/chat/completions 与 /chat/completions、/v1/messages 与 /messages 等价）；透明转发下 OpenAI 与 Anthropic 协议客户端可共用同一令牌。"
        />
      </Card>

      <AIHelpCard origin={origin} />

      <Card>
        <Tabs
          items={[
            {
              key: 'claude',
              label: 'Claude Code',
              children: (
                <div>
                  <Typography.Title level={5} style={{ marginTop: 0 }}>推荐：写入 ~/.claude/settings.json</Typography.Title>
                  <CodeBlock text={`{\n  "env": {\n    "ANTHROPIC_BASE_URL": "${origin}",\n    "ANTHROPIC_AUTH_TOKEN": "${TOKEN_PLACEHOLDER}"\n  }\n}`} />
                  <Note>密钥直接落在配置文件里，一次写入长期生效（内网环境推荐）；保存后重启 Claude Code。</Note>
                  <Typography.Title level={5}>临时会话：环境变量</Typography.Title>
                  <CodeBlock text={`export ANTHROPIC_BASE_URL=${origin}\nexport ANTHROPIC_AUTH_TOKEN=${TOKEN_PLACEHOLDER}\n\nclaude`} />
                  <Note>BASE_URL 不带 <Typography.Text code>/v1</Typography.Text>，客户端会自动拼接 <Typography.Text code>/v1/messages</Typography.Text>；模型名填渠道中配置的名称（如 <Typography.Text code>claude-sonnet-4.5</Typography.Text>）。</Note>
                </div>
              ),
            },
            {
              key: 'codex',
              label: 'Codex',
              children: (
                <div>
                  <Typography.Title level={5} style={{ marginTop: 0 }}>配置文件 ~/.codex/config.toml</Typography.Title>
                  <CodeBlock text={`model = "gpt-5.2"                 # 改成渠道中配置的模型名\nmodel_provider = "keyway"\n\n[model_providers.keyway]\nname = "keyway"\nbase_url = "${origin}/v1"      # 填 ${origin} 也可以\nwire_api = "chat"                # 网关走 chat/completions，必须为 chat\nexperimental_bearer_token = "${TOKEN_PLACEHOLDER}"   # 密钥直接写入（内网推荐）`} />
                  <Note>
                    <Typography.Text code>experimental_bearer_token</Typography.Text> 把密钥直接写在配置文件里，一次写入长期生效；
                    公网环境可改用官方推荐的 <Typography.Text code>env_key = "KEYWAY_API_KEY"</Typography.Text> + 环境变量。
                    <Typography.Text code>wire_api</Typography.Text> 必须为 <Typography.Text code>chat</Typography.Text>（网关暂未实现 Responses API）。
                  </Note>
                </div>
              ),
            },
            {
              key: 'opencode',
              label: 'opencode',
              children: (
                <div>
                  <Typography.Title level={5} style={{ marginTop: 0 }}>配置文件 opencode.json（项目根目录或 ~/.config/opencode/）</Typography.Title>
                  <CodeBlock text={`{\n  "$schema": "https://opencode.ai/config.json",\n  "provider": {\n    "keyway": {\n      "npm": "@ai-sdk/openai",\n      "name": "Keyway",\n      "options": {\n        "baseURL": "${origin}/v1",\n        "apiKey": "${TOKEN_PLACEHOLDER}"\n      },\n      "models": {\n        "gpt-5.2": {}\n      }\n    },\n    "keyway-anthropic": {\n      "npm": "@ai-sdk/anthropic",\n      "name": "Keyway (Anthropic)",\n      "options": {\n        "baseURL": "${origin}/v1",\n        "apiKey": "${TOKEN_PLACEHOLDER}"\n      },\n      "models": {\n        "claude-sonnet-4.5": {}\n      }\n    }\n  },\n  "model": "keyway/gpt-5.2"\n}`} />
                  <Note>
                    优先使用 <Typography.Text code>@ai-sdk/openai</Typography.Text> / <Typography.Text code>@ai-sdk/anthropic</Typography.Text>
                    两个包（OpenAI 与 Anthropic 协议各一个 provider，共用同一令牌）；<Typography.Text code>apiKey</Typography.Text> 直接写入配置文件（内网推荐），
                    也可写 <Typography.Text code>{'{env:KEYWAY_API_KEY}'}</Typography.Text> 改用环境变量。
                    <Typography.Text code>models</Typography.Text> 中列出渠道实际配置的模型名。
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
                  <CodeBlock text={`Base URL: ${origin}/v1    # 填 ${origin} 也可以\nAPI Key:  ${TOKEN_PLACEHOLDER}`} />
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
