import React from 'react'
import { Alert, Button, DatePicker, Form, Input, Modal, Popconfirm, Popover, Select, Space, Switch, Table, Tag, Typography, message } from 'antd'
import { HolderOutlined, PlusOutlined, SwitcherOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { listTokens, createToken, updateToken, revokeToken, deleteToken, revealToken, listChannels } from '../api'
import type { GatewayToken, Channel } from '../api/types'
import { formatDateTime } from '../format'
import { copyText } from '../copy'

// 令牌的渠道面板：主开关控制是否限定范围；限定时逐渠道开关 + 拖动排序（顺序即优先级）
const TokenChannels: React.FC<{ token: GatewayToken; channels: Channel[]; onChanged: () => void }> = ({ token, channels, onChanged }) => {
  const bound = token.channelIds ?? []
  const [order, setOrder] = React.useState<number[]>(bound)
  React.useEffect(() => {
    setOrder(bound)
  }, [token.id, bound.join(',')])

  const byId = React.useMemo(() => new Map(channels.map((c) => [c.id, c])), [channels])
  const restricted = order.length > 0
  const enabled = order.filter((id) => byId.has(id))
  const rest = channels.filter((c) => !order.includes(c.id)).map((c) => c.id)
  const dragFrom = React.useRef<number | null>(null)
  const [overIndex, setOverIndex] = React.useState<number | null>(null)

  const save = async (next: number[]) => {
    setOrder(next)
    try {
      await updateToken(token.id, { channelIds: next })
      onChanged()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  // 打开限定：默认选中全部渠道（按渠道优先级排序），再由用户移出不需要的
  const enableRestrict = () => {
    const all = [...channels].sort((a, b) => b.priority - a.priority).map((c) => c.id)
    if (all.length === 0) {
      message.info('暂无渠道，请先在渠道页创建')
      return
    }
    save(all)
  }

  const finishDrag = () => {
    dragFrom.current = null
    setOverIndex(null)
  }

  const onDrop = (to: number) => {
    const from = dragFrom.current
    finishDrag()
    if (from == null || from === to) return
    const next = [...enabled]
    const [moved] = next.splice(from, 1)
    next.splice(to, 0, moved)
    save(next)
  }

  const rowStyle: React.CSSProperties = {
    display: 'flex', alignItems: 'center', gap: 10, padding: '6px 10px',
    border: '1px solid #e3e9eb', borderRadius: 6, marginBottom: 6, background: '#fbfcfd',
  }
  const handleStyle: React.CSSProperties = { cursor: 'grab', display: 'inline-flex', padding: '0 2px' }

  if (channels.length === 0) {
    return <span style={{ color: '#999' }}>暂无渠道，请先在渠道页创建后再回来配置。</span>
  }
  return (
    <div style={{ maxWidth: 560 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
        <Switch checked={restricted} onChange={(on) => (on ? enableRestrict() : save([]))} />
        <span style={{ fontWeight: 500 }}>限定渠道范围</span>
        <span style={{ color: '#888', fontSize: 12 }}>关闭 = 路由到所有启用渠道；开启 = 只用下方渠道，顺序即优先级</span>
      </div>
      {!restricted ? (
        <Alert
          type="info"
          showIcon
          message="未限定渠道：令牌可路由到所有启用渠道，按渠道优先级路由。打开开关可限定为指定渠道。"
        />
      ) : (
        <>
          <div style={{ color: '#888', fontSize: 12, marginBottom: 6 }}>
            拖动左侧图标调整该令牌的路由优先级（自上而下依次尝试）；关闭开关将渠道移出该令牌（全部移出后等同不限定）。
          </div>
          {enabled.map((id, i) => {
            const c = byId.get(id)!
            return (
              <div
                key={id}
                onDragOver={(e) => {
                  e.preventDefault()
                  setOverIndex(i)
                }}
                onDrop={() => onDrop(i)}
                onDragLeave={() => setOverIndex((v) => (v === i ? null : v))}
                style={{ ...rowStyle, borderTop: overIndex === i ? '2px solid #176b87' : undefined }}
              >
                <span
                  draggable
                  onDragStart={() => (dragFrom.current = i)}
                  onDragEnd={finishDrag}
                  style={handleStyle}
                  title="拖动排序"
                >
                  <HolderOutlined style={{ color: '#176b87' }} />
                </span>
                <span style={{ flex: 1 }}>{c.name}</span>
                {c.enabled ? null : <Tag>渠道停用</Tag>}
                <Switch size="small" checked onChange={() => save(enabled.filter((x) => x !== id))} />
              </div>
            )
          })}
          <div
            onDragOver={(e) => {
              e.preventDefault()
              setOverIndex(enabled.length)
            }}
            onDrop={() => onDrop(enabled.length)}
            onDragLeave={() => setOverIndex((v) => (v === enabled.length ? null : v))}
            style={{
              height: 10,
              marginBottom: 6,
              borderRadius: 4,
              background: overIndex === enabled.length ? 'rgba(23,107,135,.15)' : 'transparent',
            }}
          />
          {rest.length > 0 ? (
            <>
              <div style={{ color: '#888', fontSize: 12, margin: '4px 0 6px' }}>未限定（打开开关加入）</div>
              {rest.map((id) => {
                const c = byId.get(id)!
                return (
                  <div key={id} style={{ ...rowStyle, borderStyle: 'dashed', color: '#888' }}>
                    <HolderOutlined style={{ color: '#c5ced3' }} />
                    <span style={{ flex: 1 }}>{c.name}</span>
                    {c.enabled ? null : <Tag>渠道停用</Tag>}
                    <Switch size="small" checked={false} onChange={() => save([...enabled, id])} />
                  </div>
                )
              })}
            </>
          ) : null}
        </>
      )}
    </div>
  )
}

const TokensPage: React.FC = () => {
  const nav = useNavigate()
  const [tokens, setTokens] = React.useState<GatewayToken[]>([])
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [created, setCreated] = React.useState<string | null>(null)
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
      if (await copyText(r.plaintext)) {
        message.success('已复制到剪贴板')
      } else {
        message.error('复制失败，请重试')
      }
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <div className="page-heading">
        <div><h2>网关令牌</h2><p>为客户端创建访问凭证；「限定渠道」列可点选开关、拖动控制优先级。</p></div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={() => { form.resetFields(); form.setFieldsValue({ name: '', channelIds: [], modelScope: '', expiresAt: undefined }); setModalOpen(true) }}>
          新建令牌
        </Button>
      </div>
      </div>
      <Table<GatewayToken>
        rowKey="id"
        loading={loading}
        dataSource={tokens}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '前缀', dataIndex: 'keyPrefix' },
          {
            title: '限定渠道',
            dataIndex: 'channelIds',
            width: 230,
            render: (_: number[] | undefined, t: GatewayToken) => {
              const ids = t.channelIds ?? (t.channelId ? [t.channelId] : [])
              const names = ids.map((id) => channels.find((c) => c.id === id)?.name ?? `#${id}`)
              const head = names.slice(0, 2).join('、')
              const summary = ids.length === 0 ? '不限（全部渠道）' : names.length > 2 ? `${head} 等 ${names.length} 个` : head
              if (t.revoked) return <span style={{ color: '#999' }}>{summary}</span>
              return (
                <Popover
                  trigger="click"
                  placement="rightTop"
                  overlayStyle={{ maxWidth: 600 }}
                  title={`渠道范围与顺序：${t.name}`}
                  content={<TokenChannels token={t} channels={channels} onChanged={refresh} />}
                >
                  <a>
                    {summary} <SwitcherOutlined style={{ color: '#176b87', marginLeft: 4 }} />
                  </a>
                </Popover>
              )
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
                {t.revoked ? (
                  <Popconfirm
                    title="删除该令牌记录？删除后不可恢复"
                    onConfirm={async () => {
                      await deleteToken(t.id)
                      message.success('已删除')
                      refresh()
                    }}
                  >
                    <a style={{ color: 'red' }}>删除</a>
                  </Popconfirm>
                ) : (
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
          <Form.Item name="channelIds" label="限定渠道" extra={<span className="form-hint">留空则允许访问所有启用渠道；创建后也可在列表「限定渠道」中配置开关与顺序。</span>}>
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
        open={created !== null}
        title="令牌已创建"
        onCancel={() => setCreated(null)}
        onOk={() => setCreated(null)}
      >
        <Typography.Paragraph>请复制保存；之后也可在令牌列表中复制：</Typography.Paragraph>
        <Typography.Paragraph copyable={{ text: created ?? '' }} code>
          {created}
        </Typography.Paragraph>
        <Typography.Paragraph style={{ marginBottom: 0 }}>
          客户端配置方法见 <a onClick={() => { setCreated(null); nav('/guide') }}>接入指南</a>。
        </Typography.Paragraph>
      </Modal>
    </div>
  )
}

export default TokensPage
