import React from 'react'
import { Alert, Button, DatePicker, Form, Input, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Typography, message } from 'antd'
import { HolderOutlined, PlusOutlined } from '@ant-design/icons'
import { listTokens, createToken, updateToken, revokeToken, revealToken, listChannels } from '../api'
import type { GatewayToken, Channel } from '../api/types'
import { formatDateTime } from '../format'

// 令牌的渠道面板：开关控制令牌可否路由到该渠道，拖动控制令牌内优先级
const TokenChannels: React.FC<{ token: GatewayToken; channels: Channel[]; onChanged: () => void }> = ({ token, channels, onChanged }) => {
  const bound = token.channelIds ?? []
  const [order, setOrder] = React.useState<number[]>(bound)
  React.useEffect(() => {
    setOrder(bound)
  }, [token.id, bound.join(',')])

  const byId = React.useMemo(() => new Map(channels.map((c) => [c.id, c])), [channels])
  const enabled = order.filter((id) => byId.has(id))
  const disabled = channels.filter((c) => !order.includes(c.id)).map((c) => c.id)
  const dragFrom = React.useRef<number | null>(null)

  const save = async (next: number[]) => {
    setOrder(next)
    try {
      await updateToken(token.id, { channelIds: next })
      onChanged()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const onDrop = (to: number) => {
    const from = dragFrom.current
    dragFrom.current = null
    if (from == null || from === to) return
    const next = [...enabled]
    const [moved] = next.splice(from, 1)
    next.splice(to, 0, moved)
    save(next)
  }

  if (channels.length === 0) {
    return <span style={{ color: '#999' }}>暂无渠道，请先在渠道页创建后再回来配置。</span>
  }
  return (
    <div style={{ maxWidth: 540 }}>
      {enabled.length === 0 ? (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 8 }}
          message="未限定渠道：当前对所有启用渠道生效（按渠道优先级路由）。打开任一开关即限定为所选渠道。"
        />
      ) : (
        <div style={{ color: '#888', fontSize: 12, marginBottom: 8 }}>自上而下为该令牌的路由优先级，拖动调整；开关控制令牌可否路由到对应渠道。</div>
      )}
      {enabled.map((id, i) => {
        const c = byId.get(id)!
        return (
          <div
            key={id}
            draggable
            onDragStart={() => (dragFrom.current = i)}
            onDragOver={(e) => e.preventDefault()}
            onDrop={() => onDrop(i)}
            style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '6px 10px', border: '1px solid #eee', borderRadius: 6, marginBottom: 6, background: '#fafafa', cursor: 'grab' }}
          >
            <HolderOutlined style={{ color: '#999' }} />
            <span style={{ flex: 1 }}>{c.name}</span>
            {c.enabled ? null : <Tag>渠道停用</Tag>}
            <Switch size="small" checked onChange={() => save(enabled.filter((x) => x !== id))} />
          </div>
        )
      })}
      {disabled.map((id) => {
        const c = byId.get(id)!
        return (
          <div key={id} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '6px 10px', border: '1px dashed #eee', borderRadius: 6, marginBottom: 6, color: '#888' }}>
            <HolderOutlined style={{ color: '#ddd' }} />
            <span style={{ flex: 1 }}>{c.name}</span>
            {c.enabled ? null : <Tag>渠道停用</Tag>}
            <Switch size="small" checked={false} onChange={() => save([...enabled, id])} />
          </div>
        )
      })}
    </div>
  )
}

const TokensPage: React.FC = () => {
  const [tokens, setTokens] = React.useState<GatewayToken[]>([])
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [created, setCreated] = React.useState<string | null>(null)
  const [revealed, setRevealed] = React.useState<string | null>(null)
  const [form] = Form.useForm()

  const refresh = React.useCallback(() => {
    setLoading(true)
    Promise.all([listTokens(), listChannels()])
      .then(([t, c]) => {
        setTokens(t.tokens)
        setChannels(c.channels)
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  React.useEffect(refresh, [refresh])

  const submit = async () => {
    const v = await form.validateFields()
    try {
      const r = await createToken({
        name: v.name,
        channelIds: v.channelIds,
        modelScope: v.modelScope,
        expiresAt: v.expiresAt?.toISOString(),
      })
      setModalOpen(false)
      setCreated(r.plaintext)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const copyKey = async (t: GatewayToken) => {
    try {
      const r = await revealToken(t.id)
      try {
        await navigator.clipboard.writeText(r.plaintext)
        message.success('已复制到剪贴板')
      } catch {
        // 剪贴板不可用（如非安全上下文）时回退为弹窗展示
        setRevealed(r.plaintext)
      }
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <div className="page-heading">
        <div><h2>网关令牌</h2><p>为客户端创建访问凭证；展开行可点选控制渠道开关、拖动控制优先级。</p></div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={() => { form.resetFields(); form.setFieldsValue({ name: '', channelIds: [], modelScope: '', expiresAt: undefined }); setModalOpen(true) }}>
          新建令牌
        </Button>
      </div>
      </div>
      <Table<GatewayToken>
        rowKey="id"
        loading={loading}
        dataSource={tokens}
        expandable={{
          expandedRowRender: (t) =>
            t.revoked ? (
              <span style={{ color: '#999' }}>令牌已吊销，无需配置渠道。</span>
            ) : (
              <TokenChannels token={t} channels={channels} onChanged={refresh} />
            ),
        }}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '前缀', dataIndex: 'keyPrefix' },
          {
            title: '限定渠道',
            dataIndex: 'channelIds',
            render: (_: number[] | undefined, t: GatewayToken) => {
              const ids = t.channelIds ?? (t.channelId ? [t.channelId] : [])
              if (ids.length === 0) return '不限'
              return ids
                .map((id) => channels.find((c) => c.id === id)?.name ?? `#${id}`)
                .join('、')
            },
          },
          { title: '模型范围', dataIndex: 'modelScope', render: (v?: string) => v ?? '不限' },
          { title: '创建时间', dataIndex: 'createdAt', render: (v: number | string) => formatDateTime(v) },
          { title: '过期时间', dataIndex: 'expiresAt', render: (v?: number | string) => v ? formatDateTime(v) : '永不过期' },
          {
            title: '状态',
            dataIndex: 'revoked',
            render: (r: boolean) => (r ? <Tag>已吊销</Tag> : <Tag color="green">有效</Tag>),
          },
          {
            title: '操作',
            width: 200,
            render: (_, t) =>
              <Space>
                <a onClick={() => copyKey(t)}>复制密钥</a>
                {t.revoked ? null : (
                  <Popconfirm
                    title="吊销后立即生效？"
                    onConfirm={async () => {
                      await revokeToken(t.id)
                      message.success('已吊销')
                      refresh()
                    }}
                  >
                    <a style={{ color: 'red' }}>吊销</a>
                  </Popconfirm>
                )}
              </Space>,
          },
        ]}
      />
      <Modal title="新建令牌" open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input placeholder="如 claude-code" />
          </Form.Item>
          <Form.Item name="channelIds" label="限定渠道" extra={<span className="form-hint">留空则允许访问所有启用渠道；创建后可在列表展开行中开关与排序。</span>}>
            <Select
              mode="multiple"
              allowClear
              options={channels.map((c) => ({ value: c.id, label: c.name }))}
              placeholder="不限定则路由到所有启用渠道"
            />
          </Form.Item>
          <Form.Item name="modelScope" label="模型范围（可选）" extra={<span className="form-hint">支持前缀通配，例如 claude-*。</span>}>
            <Input placeholder="如 claude-*" />
          </Form.Item>
          <Form.Item name="expiresAt" label="过期时间（可选）">
            <DatePicker showTime style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>
      <Modal
        open={revealed !== null}
        title="令牌密钥"
        onCancel={() => setRevealed(null)}
        onOk={() => setRevealed(null)}
      >
        <Typography.Paragraph>剪贴板不可用，请手动复制：</Typography.Paragraph>
        <Typography.Paragraph copyable={{ text: revealed ?? '' }} code>
          {revealed}
        </Typography.Paragraph>
      </Modal>
      <Modal
        open={created !== null}
        title="令牌已创建"
        onCancel={() => setCreated(null)}
        onOk={() => setCreated(null)}
      >
        <Typography.Paragraph>请复制保存；之后也可在令牌列表中复制：</Typography.Paragraph>
        <Typography.Paragraph copyable={{ text: created ?? '' }} code>
          {created}
        </Typography.Paragraph>
      </Modal>
    </div>
  )
}

export default TokensPage
