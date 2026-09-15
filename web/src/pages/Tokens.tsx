import React from 'react'
import { Alert, Button, DatePicker, Form, Input, Modal, Popconfirm, Popover, Select, Space, Switch, Table, Tag, Typography, message } from 'antd'
import { CaretDownOutlined, CaretUpOutlined, DownOutlined, HolderOutlined, PlusOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { listTokens, createToken, updateToken, revokeToken, deleteToken, revealToken, listChannels } from '../api'
import type { GatewayToken, Channel } from '../api/types'
import { formatDateTime } from '../format'
import { copyText } from '../copy'

// 会话级顺序记忆：令牌 → 最近一次非空绑定顺序（仅存内存，刷新后回到服务端状态）。
// 用于：① 移出的渠道重新加入时按原位置恢复，开关不改变优先级；
// ② 主开关关闭后重开时恢复上次集合与顺序，而不是重置为全选。
const orderMemory = new Map<number, number[]>()

// 令牌的渠道面板：主开关控制是否限定范围；限定时逐渠道开关 + 拖动/上下移排序（顺序即优先级）
const TokenChannels: React.FC<{ token: GatewayToken; channels: Channel[]; onChanged: () => void }> = ({ token, channels, onChanged }) => {
  const bound = token.channelIds ?? []
  const [order, setOrder] = React.useState<number[]>(bound)
  React.useEffect(() => {
    setOrder(bound)
    if (bound.length > 0) orderMemory.set(token.id, bound)
  }, [token.id, bound.join(',')])

  const byId = React.useMemo(() => new Map(channels.map((c) => [c.id, c])), [channels])
  const restricted = order.length > 0
  const enabled = order.filter((id) => byId.has(id))
  const rest = channels.filter((c) => !order.includes(c.id)).map((c) => c.id)
  const dragFrom = React.useRef<number | null>(null)
  const [dragging, setDragging] = React.useState<number | null>(null)
  const [overIndex, setOverIndexState] = React.useState<number | null>(null)
  const overRef = React.useRef<number | null>(null)
  const setOverIndex = (v: number | null) => {
    overRef.current = v
    setOverIndexState(v)
  }

  const save = async (next: number[]) => {
    setOrder(next)
    if (next.length > 0) orderMemory.set(token.id, next)
    try {
      await updateToken(token.id, { channelIds: next })
      onChanged()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  // 打开限定：恢复上次使用的集合与顺序；从未配置过才默认全选（按渠道优先级排序）
  const enableRestrict = () => {
    if (channels.length === 0) {
      message.info('暂无渠道，请先在渠道页创建')
      return
    }
    const mem = (orderMemory.get(token.id) ?? []).filter((id) => byId.has(id))
    save(mem.length > 0 ? mem : [...channels].sort((a, b) => b.priority - a.priority).map((c) => c.id))
  }

  // 重新加入的渠道按记忆位置插回，避免开关导致优先级漂移
  const addChannel = (id: number) => {
    const mem = orderMemory.get(token.id)
    const idx = mem ? mem.indexOf(id) : -1
    const pos = idx >= 0 ? Math.min(idx, enabled.length) : enabled.length
    save([...enabled.slice(0, pos), id, ...enabled.slice(pos)])
  }

  const finishDrag = () => {
    dragFrom.current = null
    setDragging(null)
    setOverIndex(null)
  }

  // 拖到行上半区 = 插到该行之前，下半区 = 插到之后（末行下半区即落到末尾）
  const onRowDragOver = (e: React.DragEvent<HTMLDivElement>, i: number) => {
    e.preventDefault()
    const rect = e.currentTarget.getBoundingClientRect()
    setOverIndex(e.clientY < rect.top + rect.height / 2 ? i : i + 1)
  }

  const onDrop = () => {
    const from = dragFrom.current
    let to = overRef.current
    finishDrag()
    if (from == null || to == null) return
    // to 为移除前的插入位（行上半区 = 该行之前，下半区 = 之后）；移除自身后向下拖需前移一位
    if (to > from) to -= 1
    const next = [...enabled]
    const [moved] = next.splice(from, 1)
    next.splice(to, 0, moved)
    if (next.join(',') !== enabled.join(',')) save(next)
  }

  const move = (from: number, to: number) => {
    if (to < 0 || to >= enabled.length || from === to) return
    const next = [...enabled]
    const [m] = next.splice(from, 1)
    next.splice(to, 0, m)
    save(next)
  }

  const rowStyle: React.CSSProperties = {
    display: 'flex', alignItems: 'center', gap: 10, padding: '6px 10px',
    border: '1px solid #e3e9eb', borderRadius: 6, marginBottom: 6, background: '#fbfcfd',
  }

  if (channels.length === 0) {
    return <span style={{ color: '#999' }}>暂无渠道，请先在渠道页创建后再回来配置。</span>
  }

  // 落点指示用 boxShadow 画在行边缘，不占布局空间、无抖动
  const insertShadow = (i: number): React.CSSProperties => {
    if (overIndex === i) return { boxShadow: 'inset 0 2px 0 #176b87' }
    if (overIndex === i + 1 && i === enabled.length - 1) return { boxShadow: 'inset 0 -2px 0 #176b87' }
    return {}
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
            整行可拖动，或用 ↑↓ 按钮调整优先级（自上而下依次尝试）；移出的渠道重新加入时回到原位置。
          </div>
          {enabled.map((id, i) => {
            const c = byId.get(id)!
            return (
              <div
                key={id}
                draggable
                onDragStart={() => {
                  dragFrom.current = i
                  setDragging(i)
                }}
                onDragEnd={finishDrag}
                onDragOver={(e) => onRowDragOver(e, i)}
                onDrop={onDrop}
                onDragLeave={() => {
                  const v = overRef.current
                  if (v === i || v === i + 1) setOverIndex(null)
                }}
                style={{ ...rowStyle, ...insertShadow(i), cursor: 'grab', opacity: dragging === i ? 0.45 : undefined }}
                title="拖动整行调整顺序"
              >
                <HolderOutlined style={{ color: '#176b87' }} />
                <span style={{ flex: 1 }}>{c.name}</span>
                {c.enabled ? null : <Tag>渠道停用</Tag>}
                <Space size={2}>
                  <Button type="text" size="small" icon={<CaretUpOutlined />} disabled={i === 0} onClick={() => move(i, i - 1)} title="上移" />
                  <Button type="text" size="small" icon={<CaretDownOutlined />} disabled={i === enabled.length - 1} onClick={() => move(i, i + 1)} title="下移" />
                </Space>
                <Switch size="small" checked onChange={() => save(enabled.filter((x) => x !== id))} title="移出该令牌" />
              </div>
            )
          })}
          {rest.length > 0 ? (
            <>
              <div style={{ color: '#888', fontSize: 12, margin: '4px 0 6px' }}>未限定（打开开关加入，加入后按原位置恢复）</div>
              {rest.map((id) => {
                const c = byId.get(id)!
                return (
                  <div key={id} style={{ ...rowStyle, borderStyle: 'dashed', color: '#888' }}>
                    <PlusOutlined style={{ color: '#c5ced3' }} />
                    <span style={{ flex: 1 }}>{c.name}</span>
                    {c.enabled ? null : <Tag>渠道停用</Tag>}
                    <Switch size="small" checked={false} onChange={() => addChannel(id)} />
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

  // 已揭示的令牌明文缓存（仅存本组件 ref，不渲染）：指针按下时预取，
  // 点击复制若命中缓存即可在用户手势内同步执行剪贴板写入，
  // 避免 await 网络请求后丢失手势（Firefox / 部分内嵌浏览器会判定剪贴板不可用）
  const revealedRef = React.useRef(new Map<number, string>())

  const prefetchKey = (id: number) => {
    if (revealedRef.current.has(id)) return
    revealToken(id)
      .then((r) => revealedRef.current.set(id, r.plaintext))
      .catch(() => {})
  }

  const copyKey = async (t: GatewayToken) => {
    try {
      let plaintext = revealedRef.current.get(t.id)
      if (!plaintext) {
        plaintext = (await revealToken(t.id)).plaintext
        revealedRef.current.set(t.id, plaintext)
      }
      if (await copyText(plaintext)) {
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
        <div><h2>网关令牌</h2><p>为客户端创建访问凭证；「限定渠道」列点开即可配置范围与优先级。</p></div>
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
                  <a className="channel-pill" title="点击配置渠道范围与优先级">
                    <span className="channel-pill-text">{summary}</span>
                    <DownOutlined />
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
                <a onClick={() => copyKey(t)} onPointerDown={() => prefetchKey(t.id)} onMouseEnter={() => prefetchKey(t.id)}>复制密钥</a>
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
          <Form.Item name="channelIds" label="限定渠道" extra={<span className="form-hint">留空则允许访问所有启用渠道；创建后可在列表「限定渠道」中调整范围与优先级。</span>}>
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
