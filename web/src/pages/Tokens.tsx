import React from 'react'
import { Alert, Button, DatePicker, Form, Input, Modal, Popconfirm, Popover, Select, Space, Switch, Table, Tag, Typography, message } from 'antd'
import { CaretDownOutlined, CaretUpOutlined, DownOutlined, HolderOutlined, PlusOutlined } from '@ant-design/icons'
import { DndContext, PointerSensor, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core'
import { SortableContext, arrayMove, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { useNavigate } from 'react-router-dom'
import { listTokens, createToken, updateToken, revokeToken, deleteToken, revealToken, listChannels } from '../api'
import type { GatewayToken, Channel } from '../api/types'
import { formatDateTime } from '../format'
import { copyText } from '../copy'
import { useI18n } from '../i18n'

// 渠道行样式（渠道面板列表行）
const rowStyle: React.CSSProperties = {
  display: 'flex', alignItems: 'center', gap: 10, padding: '6px 10px',
  border: '1px solid var(--kw-border)', borderRadius: 6, marginBottom: 6, background: 'var(--kw-soft-bg)',
}
// 已关闭渠道：原位保留、置灰显示（顺序不变，只是不参与路由）
const closedStyle: React.CSSProperties = { color: 'var(--kw-tertiary)', background: 'var(--kw-soft-bg)' }

// 可排序渠道行：拖拽只认行首手柄（dnd-kit listeners 绑定在手柄上），
// 名称/开关区域不可拖，天然杜绝误触；拖动时其余行自动让位（transform 动画）
const ChannelRow: React.FC<{
  c: Channel
  on: boolean
  i: number
  total: number
  onToggle: (id: number, on: boolean) => void
  onMove: (from: number, to: number) => void
}> = ({ c, on, i, total, onToggle, onMove }) => {
  const { t } = useI18n()
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: c.id })
  return (
    <div
      ref={setNodeRef}
      style={{ ...rowStyle, ...(on ? {} : closedStyle), transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.5 : undefined }}
    >
      <span
        {...attributes}
        {...listeners}
        style={{ display: 'inline-flex', alignItems: 'center', cursor: 'grab', padding: '6px 6px 6px 2px', marginLeft: -8 }}
        title={t('tokens.dragToReorder')}
      >
        <HolderOutlined style={{ color: on ? 'var(--kw-primary)' : 'var(--kw-tertiary)' }} />
      </span>
      <span style={{ flex: 1 }}>{c.name}</span>
      {c.enabled ? null : <Tag>{t('tokens.channelDisabled')}</Tag>}
      <Space size={2}>
        <Button type="text" size="small" icon={<CaretUpOutlined />} disabled={i === 0} onClick={() => onMove(i, i - 1)} title={t('tokens.moveUp')} />
        <Button type="text" size="small" icon={<CaretDownOutlined />} disabled={i === total - 1} onClick={() => onMove(i, i + 1)} title={t('tokens.moveDown')} />
      </Space>
      <Switch size="small" checked={on} onChange={(v) => onToggle(c.id, v)} title={on ? t('tokens.closeKeepsPosition') : t('tokens.open')} />
    </div>
  )
}

// 令牌的渠道面板：顺序（channelOrder，含已关闭渠道）与启用集合（channelIds，路由范围）
// 分离——关闭渠道只改启用集合，渠道保持原位、顺序不变。
// 面板没有"限定"主开关：令牌始终按列表顺序路由；存量"不限"令牌（启用集合为空）
// 只作为只读过渡态——以全部渠道按优先级预览，任何调整都会把令牌固化为所选渠道，
// 从此不再有两套优先级规则的歧义。面板操作走乐观更新 + onSaved 静默更新列表。
const TokenChannels: React.FC<{ token: GatewayToken; channels: Channel[]; onSaved: (t: GatewayToken) => void }> = ({ token, channels, onSaved }) => {
  const { t } = useI18n()
  const boundIds = token.channelIds ?? []
  const boundOrder = token.channelOrder ?? []
  // 服务端 order 为空（旧数据）时用启用集合兜底，保证顺序信息自愈
  const mergeBound = (o: number[], ids: number[]) => {
    const seen = new Set(o)
    return [...o, ...ids.filter((id) => !seen.has(id))]
  }
  const [order, setOrder] = React.useState<number[]>(() => mergeBound(boundOrder, boundIds))
  const [ids, setIds] = React.useState<number[]>(boundIds)
  // unrestricted = 服务端 restricted=false（不限，含创建留空与旧数据）：面板以预览呈现，
  // 不落库；首次任何调整（开关/排序/加入）即连同 restricted=true 固化为限定集合
  const unrestricted = !token.restricted
  React.useEffect(() => {
    if (unrestricted) {
      // 预览顺序：尊重已保存的面板顺序（旧数据），否则按渠道优先级；全部预览为开启
      const base = mergeBound(boundOrder, boundIds)
      const all = base.length > 0 ? base : [...channels].sort((a, b) => b.priority - a.priority).map((c) => c.id)
      if (all.length > 0) {
        setOrder(all)
        setIds(all)
        return
      }
    }
    setIds(boundIds)
    setOrder(mergeBound(boundOrder, boundIds))
  }, [token.id, token.restricted, boundIds.join(','), boundOrder.join(','), channels.length])

  const byId = React.useMemo(() => new Map(channels.map((c) => [c.id, c])), [channels])
  const idSet = React.useMemo(() => new Set(ids), [ids])
  // 渲染与排序都基于存活渠道（渠道被删除后从顺序中自然剔除）
  const rows = React.useMemo(() => order.filter((id) => byId.has(id)), [order, byId])
  const rest = channels.filter((c) => !rows.includes(c.id)).map((c) => c.id)

  // 按压移动 4px 才进入拖拽：点击开关/按钮不会被解读为拖动
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 4 } }))

  // 面板操作一律为限定语义（restricted=true）：启用集合可为空 = 限定范围内全部
  // 临时停用（路由零候选，回退默认渠道/404），与吊销（永久失效）语义分离
  const save = async (nextIds: number[], nextOrder: number[]) => {
    const prevIds = ids
    const prevOrder = order
    setIds(nextIds)
    setOrder(nextOrder)
    try {
      const r = await updateToken(token.id, { channelIds: nextIds, channelOrder: nextOrder, restricted: true })
      onSaved(r.token)
    } catch (e) {
      setIds(prevIds)
      setOrder(prevOrder)
      message.error((e as Error).message)
    }
  }

  // 渠道开关：只增删启用集合，顺序保持不变（关闭的渠道原位保留，支持全部关闭）
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

  const move = (from: number, to: number) => {
    if (to < 0 || to >= rows.length || from === to) return
    const next = arrayMove(rows, from, to)
    save(next.filter((x) => idSet.has(x)), next)
  }

  const onDragEnd = (e: DragEndEvent) => {
    const { active, over } = e
    if (!over || active.id === over.id) return
    const from = rows.findIndex((x) => x === active.id)
    const to = rows.findIndex((x) => x === over.id)
    if (from < 0 || to < 0) return
    const next = arrayMove(rows, from, to)
    save(next.filter((x) => idSet.has(x)), next)
  }

  if (channels.length === 0) {
    return <span className="text-tertiary">{t('tokens.noChannels')}</span>
  }

  return (
    <div style={{ maxWidth: 560 }}>
      {unrestricted ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 8 }}
          message={t('tokens.unrestrictedAlert')}
        />
      ) : ids.length === 0 ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 8 }}
          message={t('tokens.allClosedAlert')}
        />
      ) : (
        <div className="text-secondary" style={{ fontSize: 12, marginBottom: 6 }}>
          {t('tokens.panelSummary', { enabled: ids.length, total: rows.length })}
        </div>
      )}
      <DndContext sensors={sensors} onDragEnd={onDragEnd}>
        <SortableContext items={rows} strategy={verticalListSortingStrategy}>
          {rows.map((id, i) => (
            <ChannelRow key={id} c={byId.get(id)!} on={idSet.has(id)} i={i} total={rows.length} onToggle={toggle} onMove={move} />
          ))}
        </SortableContext>
      </DndContext>
      {rest.length > 0 ? (
        <>
          <div className="text-secondary" style={{ fontSize: 12, margin: '4px 0 6px' }}>{t('tokens.notAdded')}</div>
          {rest.map((id) => {
            const c = byId.get(id)!
            return (
              <div key={id} className="text-secondary" style={{ ...rowStyle, borderStyle: 'dashed' }}>
                <PlusOutlined style={{ color: 'var(--kw-tertiary)' }} />
                <span style={{ flex: 1 }}>{c.name}</span>
                {c.enabled ? null : <Tag>{t('tokens.channelDisabled')}</Tag>}
                <Switch size="small" checked={false} onChange={() => addNew(id)} />
              </div>
            )
          })}
        </>
      ) : null}
    </div>
  )
}

const TokensPage: React.FC = () => {
  const nav = useNavigate()
  const { t, locale } = useI18n()
  const [tokens, setTokens] = React.useState<GatewayToken[]>([])
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [created, setCreated] = React.useState<string | null>(null)
  const [renaming, setRenaming] = React.useState<GatewayToken | null>(null)
  const [form] = Form.useForm()
  const [renameForm] = Form.useForm()

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

  // 重命名：名称纯展示用途，服务端 PUT /tokens/:id 已支持 name 字段
  const submitRename = async () => {
    const v = await renameForm.validateFields()
    try {
      const r = await updateToken(renaming!.id, { name: v.name })
      applyTokenUpdate(r.token)
      setRenaming(null)
      message.success(t('common.updated'))
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

  const copyKey = async (tk: GatewayToken) => {
    try {
      let plaintext = revealedRef.current.get(tk.id)
      if (!plaintext) {
        plaintext = (await revealToken(tk.id)).plaintext
        revealedRef.current.set(tk.id, plaintext)
      }
      if (await copyText(plaintext)) {
        message.success(t('tokens.copiedToClipboard'))
      } else {
        message.error(t('tokens.copyFailed'))
      }
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <div className="page-heading">
        <div><h2>{t('tokens.title')}</h2><p>{t('tokens.subtitle')}</p></div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={() => { form.resetFields(); form.setFieldsValue({ name: '', channelIds: [], modelScope: '', expiresAt: undefined }); setModalOpen(true) }}>
          {t('tokens.create')}
        </Button>
      </div>
      </div>
      <Table<GatewayToken>
        rowKey="id"
        loading={loading}
        dataSource={tokens}
        scroll={{ x: 'max-content' }}
        locale={{ emptyText: t('tokens.empty') }}
        columns={[
          { title: t('common.name'), dataIndex: 'name' },
          { title: t('tokens.prefix'), dataIndex: 'keyPrefix', render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
          {
            title: t('tokens.channelRestriction'),
            dataIndex: 'channelIds',
            width: 230,
            render: (_: number[] | undefined, tk: GatewayToken) => {
              const ids = tk.channelIds ?? (tk.channelId ? [tk.channelId] : [])
              const names = ids.map((id) => channels.find((c) => c.id === id)?.name ?? `#${id}`)
              const head = names.slice(0, 2).join(locale === 'zh' ? '、' : ', ')
              const summary = ids.length === 0 ? (tk.restricted ? t('tokens.allClosed') : t('tokens.unrestrictedAll')) : names.length > 2 ? t('tokens.andMore', { head, count: names.length }) : head
              if (tk.revoked) return <span className="text-tertiary">{summary}</span>
              return (
                <Popover
                  trigger="click"
                  placement="rightTop"
                  overlayStyle={{ maxWidth: 600 }}
                  title={t('tokens.channelScopeOrder', { name: tk.name })}
                  content={<TokenChannels token={tk} channels={channels} onSaved={applyTokenUpdate} />}
                >
                  <a className="channel-pill" title={t('tokens.clickToConfigure')}>
                    <span className="channel-pill-text">{summary}</span>
                    <DownOutlined />
                  </a>
                </Popover>
              )
            },
          },
          { title: t('tokens.modelScope'), dataIndex: 'modelScope', render: (v?: string) => v ?? t('tokens.unrestricted') },
          { title: t('common.createdAt'), dataIndex: 'createdAt', render: (v: number | string) => formatDateTime(v) },
          { title: t('tokens.expiresAt'), dataIndex: 'expiresAt', render: (v?: number | string) => v ? formatDateTime(v) : t('tokens.neverExpires') },
          {
            title: t('common.status'),
            dataIndex: 'revoked',
            render: (r: boolean) => (r ? <Tag>{t('tokens.revoked')}</Tag> : <Tag color="green">{t('tokens.valid')}</Tag>),
          },
          {
            title: t('common.action'),
            width: 260,
            render: (_, tk) =>
              <Space>
                <Button size="small" onClick={() => copyKey(tk)} onPointerDown={() => prefetchKey(tk.id)} onMouseEnter={() => prefetchKey(tk.id)}>{t('tokens.copyKey')}</Button>
                <Button size="small" onClick={() => { setRenaming(tk); renameForm.setFieldsValue({ name: tk.name }) }}>{t('tokens.rename')}</Button>
                {tk.revoked ? (
                  <Popconfirm
                    title={t('tokens.deleteConfirm')}
                    onConfirm={async () => {
                      await deleteToken(tk.id)
                      message.success(t('common.deleted'))
                      refresh()
                    }}
                  >
                    <Button size="small" danger>{t('common.delete')}</Button>
                  </Popconfirm>
                ) : (
                  <Popconfirm
                    title={t('tokens.revokeConfirm')}
                    onConfirm={async () => {
                      await revokeToken(tk.id)
                      message.success(t('tokens.revoked'))
                      refresh()
                    }}
                  >
                    <Button size="small" danger>{t('tokens.revoke')}</Button>
                  </Popconfirm>
                )}
              </Space>,
          },
        ]}
      />
      <Modal title={t('tokens.create')} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('common.nameRequired') }]}>
            <Input placeholder={t('tokens.namePlaceholder')} />
          </Form.Item>
          <Form.Item name="channelIds" label={t('tokens.channelRestriction')} extra={<span className="form-hint">{t('tokens.channelIdsHint')}</span>}>
            <Select
              mode="multiple"
              allowClear
              options={channels.map((c) => ({ value: c.id, label: c.name }))}
              placeholder={t('tokens.channelIdsPlaceholder')}
            />
          </Form.Item>
          <Form.Item name="modelScope" label={t('tokens.modelScopeOptional')} extra={<span className="form-hint">{t('tokens.modelScopeHint')}</span>}>
            <Input placeholder={t('tokens.modelScopePlaceholder')} />
          </Form.Item>
          <Form.Item name="expiresAt" label={t('tokens.expiresAtOptional')}>
            <DatePicker showTime style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>
      <Modal title={t('tokens.renameTitle')} open={renaming !== null} onOk={submitRename} onCancel={() => setRenaming(null)} destroyOnClose>
        <Form form={renameForm} layout="vertical">
          <Form.Item
            name="name"
            label={t('common.name')}
            rules={[
              { required: true, message: t('common.nameRequired') },
              { max: 64, message: t('tokens.nameMaxLength', { max: 64 }) },
            ]}
          >
            <Input placeholder={t('tokens.namePlaceholder')} autoFocus />
          </Form.Item>
        </Form>
      </Modal>
      <Modal
        open={created !== null}
        title={t('tokens.createdTitle')}
        okText={t('tokens.savedClose')}
        onCancel={() => setCreated(null)}
        onOk={() => setCreated(null)}
      >
        <Typography.Paragraph>{t('tokens.copyPrompt')}</Typography.Paragraph>
        <Typography.Paragraph copyable={{ text: created ?? '' }} code>
          {created}
        </Typography.Paragraph>
        <Typography.Paragraph style={{ marginBottom: 0 }}>
          {t('tokens.clientConfigPrefix')} <a onClick={() => { setCreated(null); nav('/guide') }}>{t('tokens.guideLink')}</a>{t('tokens.clientConfigSuffix')}
        </Typography.Paragraph>
      </Modal>
    </div>
  )
}

export default TokensPage
