import React from 'react'
import { Alert, Button, Card, Checkbox, Col, Collapse, Row, Select, Steps, Tabs, Typography, message } from 'antd'
import { CopyOutlined, RobotOutlined } from '@ant-design/icons'
import { listTokens, revealToken } from '../api'
import type { GatewayToken } from '../api/types'
import { copyText } from '../copy'
import { useI18n } from '../i18n'

type TFunc = ReturnType<typeof useI18n>['t']

const CLIENTS = [
  { key: 'claude', label: 'Claude Code' },
  { key: 'codex', label: 'Codex' },
  { key: 'opencode', label: 'opencode' },
] as const
type ClientKey = (typeof CLIENTS)[number]['key']

const CodeBlock: React.FC<{ text: string }> = ({ text }) => {
  const { t } = useI18n()
  const copy = async () => {
    if (await copyText(text)) {
      message.success(t('common.copied'))
    } else {
      message.error(t('guide.copyFailedManual'))
    }
  }
  return (
    <div style={{ position: 'relative', background: 'var(--kw-code-bg)', borderRadius: 8, marginBottom: 12 }}>
      <pre style={{ margin: 0, padding: '12px 64px 12px 16px', color: 'var(--kw-code-ink)', fontSize: 12.5, lineHeight: 1.7, overflowX: 'auto' }}>
        {text}
      </pre>
      <Button size="small" icon={<CopyOutlined />} onClick={copy} title={t('guide.copy')} style={{ position: 'absolute', top: 8, right: 8 }} />
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
const buildAIPrompt = (t: TFunc, origin: string, plaintext: string, models: string[], clients: ClientKey[]): string => {
  const modelLine = models.length
    ? models.join(', ')
    : t('guide.aiModelsFallback', { url: `${origin}/v1/models` })
  const tasks: Partial<Record<ClientKey, string>> = {
    claude: t('guide.aiTaskClaude', { origin }),
    codex: t('guide.aiTaskCodex', { origin }),
    opencode: t('guide.aiTaskOpencode', { origin }),
  }
  const selected = CLIENTS.filter((c) => clients.includes(c.key))
  const taskLines = selected
    .map((c, i) => tasks[c.key]!.replace(/^\d+\./, `${i + 1}.`))
    .join('\n')
  const taskSection =
    selected.length > 0
      ? `\n${t('guide.aiTasksTitle')}\n${taskLines}\n`
      : `\n${t('guide.aiTasksEmpty')}\n`
  return [
    t('guide.aiPromptIntro'),
    '',
    t('guide.aiPromptInfoTitle'),
    t('guide.aiPromptOpenAIUrl', { origin }),
    t('guide.aiPromptAnthropicUrl', { origin }),
    t('guide.aiPromptTokenLine', { token: plaintext }),
    t('guide.aiPromptModelsLine', { models: modelLine }),
    taskSection,
    t('guide.aiPromptNotice'),
  ].join('\n')
}

const AIHelpCard: React.FC<{ origin: string }> = ({ origin }) => {
  const { t } = useI18n()
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
        const valid = r.tokens.filter((tk) => !tk.revoked)
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
      const text = buildAIPrompt(t, origin, plaintext, models, clients)
      if (await copyText(text)) {
        message.success(t('guide.aiCopied'))
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
      message.success(t('common.copied'))
      setCopyFailed(false)
      setRealPrompt(null)
    } else {
      message.error(t('guide.copyRetryFailed'))
    }
  }

  const toggleClient = (key: ClientKey, checked: boolean) => {
    setClients((prev) => {
      const next = checked ? [...prev, key] : prev.filter((k) => k !== key)
      return next.length > 0 ? next : prev
    })
  }

  const maskedPrompt = buildAIPrompt(t, origin, t('guide.maskedToken'), [t('guide.maskedModels')], clients)

  return (
    <Card style={{ marginBottom: 16 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
        <RobotOutlined style={{ fontSize: 20, color: 'var(--kw-primary)' }} />
        <Typography.Title level={5} style={{ margin: 0 }}>{t('guide.aiHelpTitle')}</Typography.Title>
        <div style={{ flex: 1 }} />
        <Select
          style={{ minWidth: 200 }}
          placeholder={tokens.length ? t('guide.selectToken') : t('guide.noValidTokens')}
          value={tokenId}
          onChange={setTokenId}
          options={tokens.map((tk) => ({ value: tk.id, label: t('guide.tokenOptionLabel', { name: tk.name, prefix: tk.keyPrefix }) }))}
          notFoundContent={<span>{t('guide.createTokenFirst')}</span>}
        />
        <Button type="primary" icon={<CopyOutlined />} loading={copying} disabled={!tokenId} onClick={copyPrompt}>
          {t('guide.copyPromptWithToken')}
        </Button>
      </div>
      <Row style={{ marginTop: 8 }}>
        <Col>
          <span className="text-secondary" style={{ fontSize: 12, marginRight: 12 }}>{t('guide.clientsToConfigure')}</span>
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
      <Note>{t('guide.aiHelpNote')}</Note>
      <Collapse
        ghost
        activeKey={previewOpen}
        onChange={(keys) => setPreviewOpen(keys as string[])}
        items={[
          {
            key: 'preview',
            label: copyFailed ? t('guide.previewReal') : t('guide.previewMasked'),
            children: (
              <div>
                {copyFailed ? (
                  <Alert
                    type="warning"
                    showIcon
                    style={{ marginBottom: 8 }}
                    message={t('guide.clipboardUnavailable')}
                    description={
                      <span>
                        {t('guide.clipboardDesc')}
                        <a onClick={retryCopy}>{t('guide.retryCopy')}</a>
                      </span>
                    }
                  />
                ) : null}
                <div style={{ background: 'var(--kw-soft-bg)', borderRadius: 8, padding: '8px 16px' }}>
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
  const { t } = useI18n()
  const origin = window.location.origin
  const tokenPlaceholder = t('guide.tokenPlaceholder')

  return (
    <div>
      <div className="page-heading">
        <div><h2>{t('guide.title')}</h2><p>{t('guide.subtitle')}</p></div>
      </div>

      <Card style={{ marginBottom: 16 }}>
        <Steps
          direction="vertical"
          size="small"
          current={-1}
          items={[
            {
              title: t('guide.step1Title'),
              description: (
                <>
                  {t('guide.step1DescA')}
                  <a href="#/channels">{t('guide.step1LinkChannels')}</a>
                  {t('guide.step1DescB')}
                  <a href="#/templates">{t('guide.step1LinkTemplates')}</a>
                  {t('guide.step1DescC')}
                </>
              ),
            },
            {
              title: t('guide.step2Title'),
              description: (
                <>
                  {t('guide.step2DescA')}
                  <a href="#/tokens">{t('guide.step2LinkTokens')}</a>
                  {t('guide.step2DescB')}
                  <Typography.Text code>sk-keyway-…</Typography.Text>
                  {t('guide.step2DescC')}
                </>
              ),
            },
            { title: t('guide.step3Title'), description: t('guide.step3Desc') },
          ]}
        />
        <Alert
          style={{ marginTop: 4 }}
          type="info"
          showIcon
          message={<>{t('guide.currentOrigin')}<Typography.Text code copyable>{origin}</Typography.Text></>}
          description={t('guide.originDesc')}
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
                  <Typography.Title level={5} style={{ marginTop: 0 }}>{t('guide.claudeRecommended')}</Typography.Title>
                  <CodeBlock text={`{\n  "env": {\n    "ANTHROPIC_BASE_URL": "${origin}",\n    "ANTHROPIC_AUTH_TOKEN": "${tokenPlaceholder}"\n  }\n}`} />
                  <Note>{t('guide.claudeFileNote')}</Note>
                  <Typography.Title level={5}>{t('guide.claudeEnvTitle')}</Typography.Title>
                  <CodeBlock text={`export ANTHROPIC_BASE_URL=${origin}\nexport ANTHROPIC_AUTH_TOKEN=${tokenPlaceholder}\n\nclaude`} />
                  <Note>
                    {t('guide.claudeEnvNoteA')}
                    <Typography.Text code>/v1</Typography.Text>
                    {t('guide.claudeEnvNoteB')}
                    <Typography.Text code>/v1/messages</Typography.Text>
                    {t('guide.claudeEnvNoteC')}
                    <Typography.Text code>claude-sonnet-4.5</Typography.Text>
                    {t('guide.claudeEnvNoteD')}
                  </Note>
                </div>
              ),
            },
            {
              key: 'codex',
              label: 'Codex',
              children: (
                <div>
                  <Typography.Title level={5} style={{ marginTop: 0 }}>{t('guide.codexConfigTitle')}</Typography.Title>
                  <CodeBlock text={`model = "gpt-5.2"                 # 改成渠道中配置的模型名\nmodel_provider = "keyway"\n\n[model_providers.keyway]\nname = "keyway"\nbase_url = "${origin}/v1"      # 填 ${origin} 也可以\nwire_api = "chat"                # 网关走 chat/completions，必须为 chat\nexperimental_bearer_token = "${tokenPlaceholder}"   # 密钥直接写入（内网推荐）`} />
                  <Note>
                    <Typography.Text code>experimental_bearer_token</Typography.Text>
                    {t('guide.codexNoteA')}
                    <Typography.Text code>env_key = "KEYWAY_API_KEY"</Typography.Text>
                    {t('guide.codexNoteB')}
                    <Typography.Text code>wire_api</Typography.Text>
                    {t('guide.codexNoteC')}
                    <Typography.Text code>chat</Typography.Text>
                    {t('guide.codexNoteD')}
                  </Note>
                </div>
              ),
            },
            {
              key: 'opencode',
              label: 'opencode',
              children: (
                <div>
                  <Typography.Title level={5} style={{ marginTop: 0 }}>{t('guide.opencodeConfigTitle')}</Typography.Title>
                  <CodeBlock text={`{\n  "$schema": "https://opencode.ai/config.json",\n  "provider": {\n    "keyway": {\n      "npm": "@ai-sdk/openai",\n      "name": "Keyway",\n      "options": {\n        "baseURL": "${origin}/v1",\n        "apiKey": "${tokenPlaceholder}"\n      },\n      "models": {\n        "gpt-5.2": {}\n      }\n    },\n    "keyway-anthropic": {\n      "npm": "@ai-sdk/anthropic",\n      "name": "Keyway (Anthropic)",\n      "options": {\n        "baseURL": "${origin}/v1",\n        "apiKey": "${tokenPlaceholder}"\n      },\n      "models": {\n        "claude-sonnet-4.5": {}\n      }\n    }\n  },\n  "model": "keyway/gpt-5.2"\n}`} />
                  <Note>
                    {t('guide.opencodeNoteA')}
                    <Typography.Text code>@ai-sdk/openai</Typography.Text>
                    {t('guide.opencodeNoteB')}
                    <Typography.Text code>@ai-sdk/anthropic</Typography.Text>
                    {t('guide.opencodeNoteC')}
                    <Typography.Text code>apiKey</Typography.Text>
                    {t('guide.opencodeNoteD')}
                    <Typography.Text code>{'{env:KEYWAY_API_KEY}'}</Typography.Text>
                    {t('guide.opencodeNoteE')}
                    <Typography.Text code>models</Typography.Text>
                    {t('guide.opencodeNoteF')}
                  </Note>
                </div>
              ),
            },
            {
              key: 'generic',
              label: t('guide.tabGeneric'),
              children: (
                <div>
                  <Typography.Title level={5} style={{ marginTop: 0 }}>{t('guide.genericTitle')}</Typography.Title>
                  <CodeBlock text={`Base URL: ${origin}/v1    # 填 ${origin} 也可以\nAPI Key:  ${tokenPlaceholder}`} />
                  <Typography.Title level={5}>{t('guide.connectivityCheck')}</Typography.Title>
                  <CodeBlock text={`curl ${origin}/v1/chat/completions \\\n  -H "Authorization: Bearer ${tokenPlaceholder}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"gpt-5.2","messages":[{"role":"user","content":"hi"}]}'`} />
                  <Note>
                    {t('guide.genericNoteA')}
                    <Typography.Text code>GET {origin}/v1/models</Typography.Text>
                    {t('guide.genericNoteB')}
                  </Note>
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
