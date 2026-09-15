import React from 'react'
import { Alert, Button, DatePicker, Form, Input, Modal, Popconfirm, Popover, Select, Space, Switch, Table, Tag, Typography, message } from 'antd'
import { CaretDownOutlined, CaretUpOutlined, DownOutlined, HolderOutlined, PlusOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { listTokens, createToken, updateToken, revokeToken, deleteToken, revealToken, listChannels } from '../api'
import type { GatewayToken, Channel } from '../api/types'
import { formatDateTime } from '../format'
import { copyText } from '../copy'

// 令牌的渠道面板：顺序（channelOrder，含已关闭渠道）与启用集合（channelIds，路由范围）
// 分离——关闭渠道只改启用集合，渠道保持原位、顺序不变。
// 面板操作走乐观更新 + onSaved 静默更新列表（不触发整表 loading，避免弹层抖动与卡顿）
const TokenChannels: React.FC<{ token: GatewayToken; channels: Channel[]; onSaved: (t: GatewayToken) => void }> = ({ token, channels, onSaved }) => {
  const boundIds = token.channelIds ?? []
  const boundOrder = token.channelOrder ?? []
  // 服务端 order 为空（旧数据）时用启用集合兜底，保证顺序信息自愈
  const mergeBound = (o: number[], ids: number[]) => {
    const seen = new Set(o)
    return [...o, ...ids.filter((id) => !seen.has(id))]
  }
  const [order, setOrder] = React.useState<number[]>(() => mergeBound(boundOrder, boundIds))
  const [ids, setIds] = React.useState<number[]>(boundIds)
  React.useEffect(() => {
    setIds(boundIds)
    setOrder(mergeBound(boundOrder, boundIds))
  }, [token.id, boundIds.join(','), boundOrder.join(',')])

  const byId = React.useMemo(() => new Map(channels.map((c) => [c.id, c])), [channels])
  const idSet = React.useMemo(() => new Set(ids), [ids])
  // 渲染与排序都基于存活渠道（渠道被删除后从顺序中自然剔除）
  const rows = React.useMemo(() => order.filter((id) => byId.has(id)), [order, byId])
  const rest = channels.filter((c) => !rows.includes(c.id)).map((c) => c.id)
  const restricted = ids.length > 0
  const dragFrom = React.useRef<number | null>(null)
  const [dragging, setDragging] = React.useState<number | null>(null)
  const [overIndex, setOverIndexState] = React.useState<number | null>(null)
  const overRef = React.useRef<number | null>(null)
  const setOverIndex = (v: number | null) => {
    overRef.current = v
    setOverIndexState(v)
  }
  // 点击交互控件（开关/↑↓）时抑制整行拖拽：HTML5 dragstart 的目标是行本身，
  // 无法在子控件上拦截，改为按下时标记、起拖时取消——避免点击微动误触拖拽
  const noDrag = React.useRef(false)
  const pressCtrl = {
    onPointerDown: () => {
      noDrag.current = true
    },
    onPointerUp: () => {
      noDrag.current = false
    },
    onPointerCancel: () => {
      noDrag.current = false
    },
  }

  const save = async (nextIds: number[], nextOrder: number[]) => {
    const prevIds = ids
    const prevOrder = order
    setIds(nextIds)
    setOrder(nextOrder)
    try {
      const r = await updateToken(token.id, { channelIds: nextIds, channelOrder: nextOrder })
      onSaved(r.token)
    } catch (e) {
      setIds(prevIds)
      setOrder(prevOrder)
      message.error((e as Error).message)
    }
  }

  // 打开限定：恢复已配置的顺序（含曾关闭的渠道）；从未配置过才默认全选（按渠道优先级排序）
  const enableRestrict = () => {
    if (channels.length === 0) {
      message.info('暂无渠道，请先在渠道页创建')
      return
    }
    if (rows.length > 0) {
      save(rows, rows)
      return
    }
    const all = [...channels].sort((a, b) => b.priority - a.priority).map((c) => c.id)
    save(all, all)
  }

  // 渠道开关：只增删启用集合，顺序保持不变（关闭的渠道原位保留）
  const toggle = (id: number, on: boolean) => {
    if (on) {
      const set = new Set(ids)
      set.add(id)
      save(rows.filter((x) => set.has(x)), rows)
    } else {
      save(ids.filter((x) => x !== id), rows)
    }
  }

  // 未加入的渠道开启：追加到顺序末尾
  const addNew = (id: number) => save([...ids, id], [...rows, id])

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
    const next = [...rows]
    const [moved] = next.splice(from, 1)
    next.splice(to, 0, moved)
    save(next.filter((x) => idSet.has(x)), next)
  }

  const move = (from: number, to: number) => {
    if (to < 0 || to >= rows.length || from === to) return
    const next = [...rows]
    const [m] = next.splice(from, 1)
    next.splice(to, 0, m)
    save(next.filter((x) => idSet.has(x)), next)
  }

  const rowStyle: React.CSSProperties = {
    display: 'flex', alignItems: 'center', gap: 10, padding: '6px 10px',
    border: '1px solid #e3e9eb', borderRadius: 6, marginBottom: 6, background: '#fbfcfd',
  }
  // 已关闭渠道：原位保留、置灰显示（顺序不变，只是不参与路由）
  const closedStyle: React.CSSProperties = { color: '#8a979d', background: '#f6f8f9' }

  if (channels.length === 0) {
    return <span className="text-tertiary">暂无渠道，请先在渠道页创建后再回来配置。</span>
  }

  // 落点指示用 boxShadow 画在行边缘，不占布局空间、无抖动
  const insertShadow = (i: number): React.CSSProperties => {
    if (overIndex === i) return { boxShadow: 'inset 0 2px 0 #176b87' }
    if (overIndex === i + 1 && i === rows.length - 1) return { boxShadow: 'inset 0 -2px 0 #176b87' }
    return {}
  }

  return (
    <div style={{ maxWidth: 560 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
        <Switch checked={restricted} onChange={(on) => (on ? enableRestrict() : save([], rows))} />
        <span style={{ fontWeight: 500 }}>限定渠道范围</span>
        <span className="text-secondary" style={{ fontSize: 12 }}>关闭 = 路由到所有启用渠道；开启 = 只用下方开启的渠道，顺序即优先级</span>
      </div>
      {!restricted ? (
        <Alert
          type="info"
          showIcon
          message={`未限定渠道：令牌可路由到所有启用渠道，按渠道优先级路由。${
            rows.length > 0 ? '上次配置的渠道与顺序已保留，打开开关即恢复。' : '打开开关可限定为指定渠道。'
          }`}
        />
      ) : (
        <>
          <div className="text-secondary" style={{ fontSize: 12, marginBottom: 6 }}>
            整行可拖动，或用 ↑↓ 按钮调整优先级（自上而下依次尝试）；关闭的渠道保持原位，只是不参与路由。
          </div>
          {rows.map((id, i) => {
            const c = byId.get(id)!
            const on = idSet.has(id)
            return (
              <div
                key={id}
                draggable
                onDragStart={(e) => {
                  if (noDrag.current) {
                    e.preventDefault()
                    noDrag.current = false
                    return
                  }
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
                style={{ ...rowStyle, ...(on ? {} : closedStyle), ...insertShadow(i), cursor: 'grab', opacity: dragging === i ? 0.45 : undefined }}
                title="拖动整行调整顺序"
              >
                <HolderOutlined style={{ color: on ? '#176b87' : '#b7c4c9' }} />
                <span style={{ flex: 1 }}>{c.name}</span>
                {c.enabled ? null : <Tag>渠道停用</Tag>}
                <Space size={2}>
                  <Button {...pressCtrl} type="text" size="small" icon={<CaretUpOutlined />} disabled={i === 0} onClick={() => move(i, i - 1)} title="上移" />
                  <Button {...pressCtrl} type="text" size="small" icon={<CaretDownOutlined />} disabled={i === rows.length - 1} onClick={() => move(i, i + 1)} title="下移" />
                </Space>
                <span {...pressCtrl} style={{ display: 'inline-flex' }} title={on ? '关闭后保持原位' : '打开'}>
                  <Switch size="small" checked={on} onChange={(v) => toggle(id, v)} />
                </span>
              </div>
            )
          })}
          {rest.length > 0 ? (
            <>
              <div className="text-secondary" style={{ fontSize: 12, margin: '4px 0 6px' }}>未加入（打开开关将追加到列表末尾）</div>
              {rest.map((id) => {
                const c = byId.get(id)!
                return (
                  <div key={id} className="text-secondary" style={{ ...rowStyle, borderStyle: 'dashed' }}>
                    <PlusOutlined style={{ color: '#c5ced3' }} />
                    <span style={{ flex: 1 }}>{c.name}</span>
                    {c.enabled ? null : <Tag>渠道停用</Tag>}
                    <Switch size="small" checked={false} onChange={() => addNew(id)} />
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

  // 面板内渠道开关/排序操作：用接口返回的 token 静默更新对应行，
  // 不触发整表 loading——避免弹层抖动、开关卡顿与列宽重排
  const applyTokenUpdate = React.useCallback((t: GatewayToken) => {
    setTokens((ts) => ts.map((x) => (x.id === t.id ? t : x)))
  }, [])

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
        locale={{ emptyText: '暂无令牌，点击右上角「新建令牌」创建' }}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '前缀', dataIndex: 'keyPrefix', render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
          {
            title: '限定渠道',
            dataIndex: 'channelIds',
            width: 230,
            render: (_: number[] | undefined, t: GatewayToken) => {
              const ids = t.channelIds ?? (t.channelId ? [t.channelId] : [])
              const names = ids.map((id) => channels.find((c) => c.id === id)?.name ?? `#${id}`)
              const head = names.slice(0, 2).join('、')
              const summary = ids.length === 0 ? '不限（全部渠道）' : names.length > 2 ? `${head} 等 ${names.length} 个` : head
              if (t.revoked) return <span className="text-tertiary">{summary}</span>
              return (
                <Popover
                  trigger="click"
                  placement="rightTop"
                  overlayStyle={{ maxWidth: 600 }}
                  title={`渠道范围与顺序：${t.name}`}
                  content={<TokenChannels token={t} channels={channels} onSaved={applyTokenUpdate} />}
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
                    <a className="danger-link">删除</a>
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
                    <a className="danger-link">吊销</a>
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
        okText="我已保存，关闭"
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
