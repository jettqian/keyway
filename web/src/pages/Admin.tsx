import React from 'react'
import { Tabs, Table, Form, Switch, Input, Button, message, Modal, Popconfirm, Tag, Space, Select, InputNumber, Typography, Radio, Tooltip, AutoComplete } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import {
  adminUsers,
  adminSetUserStatus,
  adminResetPassword,
  adminPricing,
  adminUpdatePricing,
  adminDeletePricing,
  adminSyncRemotePricing,
  adminProxies,
  adminCreateProxy,
  adminUpdateProxy,
  adminDeleteProxy,
  adminTemplates,
  adminCreateTemplate,
  adminUpdateTemplate,
  adminDeleteTemplate,
  adminCatalogModels,
  adminCreateCatalogModel,
  adminUpdateCatalogModel,
  adminDeleteCatalogModel,
  adminSettings,
  adminUpdateSettings,
  adminSyncExchangeRate,
  adminStats,
} from '../api'
import type { User, ModelPricing, PricingSource, Proxy, ChannelTemplate, CatalogModel, StatsResponse, StatsGroup, AdminSettings } from '../api/types'
import { formatDateTime, fmtCost, fmtInt, fmtPrice } from '../format'
import CatalogPrice from '../components/CatalogPrice'

const UsersTab: React.FC = () => {
  const [users, setUsers] = React.useState<User[]>([])
  const [loading, setLoading] = React.useState(true)
  const refresh = React.useCallback(() => {
    setLoading(true)
    adminUsers()
      .then((r) => setUsers(r.users))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  React.useEffect(refresh, [refresh])
  return (
    <Table<User>
      rowKey="id"
      loading={loading}
      dataSource={users}
      columns={[
        { title: 'ID', dataIndex: 'id', width: 60 },
        { title: '用户名', dataIndex: 'username' },
        { title: '角色', dataIndex: 'role', render: (r: number) => (r === 100 ? <Tag color="red">管理员</Tag> : <Tag>用户</Tag>) },
        {
          title: '状态',
          dataIndex: 'status',
          render: (s: number) => (s === 1 ? <Tag color="green">正常</Tag> : <Tag color="orange">禁用</Tag>),
        },
        { title: '注册时间', dataIndex: 'createdAt', width: 180, render: (v: number | string) => formatDateTime(v) },
        { title: '最近登录', dataIndex: 'lastLoginAt', width: 180, render: (v: number | string) => formatDateTime(v) },
        {
          title: '操作',
          render: (_, u) => (
            <Space>
              <Button
                size="small"
                onClick={async () => {
                  await adminSetUserStatus(u.id, u.status === 1 ? 2 : 1)
                  message.success('已更新')
                  refresh()
                }}
              >
                {u.status === 1 ? '禁用' : '启用'}
              </Button>
              <Button
                size="small"
                onClick={async () => {
                  const r = await adminResetPassword(u.id)
                  Modal.info({ title: '新密码', content: <Typography.Text copyable>{r.password}</Typography.Text> })
                }}
              >
                重置密码
              </Button>
            </Space>
          ),
        },
      ]}
    />
  )
}

// UsersTab 中使用 Modal 展示重置密码结果

const PricingTab: React.FC = () => {
  const [pricing, setPricing] = React.useState<ModelPricing[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<ModelPricing | null>(null)
  const [importOpen, setImportOpen] = React.useState(false)
  const [importText, setImportText] = React.useState('')
  const [search, setSearch] = React.useState('')
  const [syncedAt, setSyncedAt] = React.useState('')
  const [sources, setSources] = React.useState<PricingSource[]>([])
  const [catalogNames, setCatalogNames] = React.useState<Set<string>>(new Set())
  const [form] = Form.useForm()
  const refresh = React.useCallback(() => {
    setLoading(true)
    Promise.all([adminPricing(), adminCatalogModels()])
      .then(([p, m]) => {
        setPricing(p.pricing)
        setSyncedAt(p.syncedAt ?? '')
        setSources(p.sources ?? [])
        setCatalogNames(new Set(m.models.map((c) => c.name)))
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  React.useEffect(refresh, [refresh])

  // 目录内模型置前显示，其余按名称排后；支持按模型名搜索筛选
  const rows = React.useMemo(() => {
    const kw = search.trim().toLowerCase()
    return pricing
      .filter((p) => !kw || p.model.toLowerCase().includes(kw))
      .sort((a, b) => {
        const inA = catalogNames.has(a.model) ? 0 : 1
        const inB = catalogNames.has(b.model) ? 0 : 1
        return inA - inB || a.model.localeCompare(b.model)
      })
  }, [pricing, search, catalogNames])

  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    setModalOpen(true)
  }
  const openEdit = (p: ModelPricing) => {
    setEditing(p)
    form.setFieldsValue({
      model: p.model, inputPerM: p.inputPerM, outputPerM: p.outputPerM,
      cachedInputPerM: p.cachedInputPerM, cacheWritePerM: p.cacheWritePerM,
    })
    setModalOpen(true)
  }
  const submit = async () => {
    const v = await form.validateFields()
    const p: ModelPricing = {
      model: editing ? editing.model : v.model,
      inputPerM: v.inputPerM ?? 0,
      cachedInputPerM: v.cachedInputPerM ?? null,
      cacheWritePerM: v.cacheWritePerM ?? null,
      outputPerM: v.outputPerM ?? 0,
    }
    try {
      await adminUpdatePricing(p)
      message.success(editing ? '已更新（费用快照从下一条日志生效）' : '已添加')
      setModalOpen(false)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }
  const exportJSON = () => {
    const blob = new Blob([JSON.stringify(pricing, null, 2)], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = 'keyway-pricing.json'
    a.click()
    URL.revokeObjectURL(a.href)
  }
  const runImport = async () => {
    let list: ModelPricing[]
    try {
      list = JSON.parse(importText)
      if (!Array.isArray(list)) throw new Error()
    } catch {
      message.error('不是合法的 JSON 数组')
      return
    }
    let ok = 0
    for (const p of list) {
      if (!p.model || p.inputPerM == null || p.outputPerM == null) continue
      try {
        await adminUpdatePricing({
          model: p.model, inputPerM: Number(p.inputPerM), outputPerM: Number(p.outputPerM),
          cachedInputPerM: p.cachedInputPerM == null ? null : Number(p.cachedInputPerM),
          cacheWritePerM: p.cacheWritePerM == null ? null : Number(p.cacheWritePerM),
        })
        ok++
      } catch {
        // 单条失败继续
      }
    }
    message.success(`导入 ${ok}/${list.length} 条`)
    setImportOpen(false)
    refresh()
  }
  const [syncing, setSyncing] = React.useState(false)
  const syncRemote = async () => {
    setSyncing(true)
    try {
      const r = await adminSyncRemotePricing()
      const { litellmAdded, openrouterAdded, skippedExisting, warnings } = r.result
      const parts = [`LiteLLM 新增 ${litellmAdded}、OpenRouter 新增 ${openrouterAdded}`]
      if (skippedExisting) parts.push(`已存在 ${skippedExisting} 条未覆盖`)
      message.success(parts.join('；') + (warnings?.length ? `（${warnings.join('；')}）` : ''))
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSyncing(false)
    }
  }
  return (
    <div>
      <div style={{ marginBottom: 8, display: 'flex', gap: 8, justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap' }}>
        <Space size="middle" className="text-secondary" style={{ fontSize: 13 }}>
          <span>
            {syncedAt ? (
              <>上次官方价目同步：{formatDateTime(syncedAt)}</>
            ) : (
              '尚未同步官方价目'
            )}
          </span>
          {sources.map((s) => (
            <Tooltip key={s.url} title={s.url}>
              <a href={s.url} target="_blank" rel="noreferrer">{s.name}</a>
            </Tooltip>
          ))}
        </Space>
        <Space>
          <Popconfirm
            title="从 LiteLLM / OpenRouter 同步官方价目？"
            description="只补缺：已存在的条目一律不覆盖（如需刷新某条可先删除再同步）。后台也会按 KEYWAY_PRICING_SYNC_HOURS 定期同步（默认 24 小时，0 关闭）。"
            onConfirm={syncRemote}
          >
            <Button loading={syncing}>同步官方价目</Button>
          </Popconfirm>
          <Button onClick={exportJSON}>导出 JSON</Button>
          <Button onClick={() => { setImportText(''); setImportOpen(true) }}>导入 JSON</Button>
          <Button type="primary" onClick={openCreate}>新增模型</Button>
        </Space>
      </div>
      <div style={{ marginBottom: 12 }}>
        <Input
          allowClear
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="按模型名搜索筛选"
          style={{ width: 240 }}
          prefix={<SearchOutlined />}
        />
        <span className="text-tertiary" style={{ fontSize: 12, marginLeft: 12 }}>
          共 {pricing.length} 条{search.trim() ? `，匹配 ${rows.length} 条` : ''}；模型目录中的条目置前显示
        </span>
      </div>
      <Table<ModelPricing>
        rowKey="model"
        loading={loading}
        dataSource={rows}
        pagination={{ pageSize: 20 }}
        columns={[
          {
            title: '模型',
            dataIndex: 'model',
            render: (n: string) => (
              <Space>
                {n}
                {catalogNames.has(n) ? <Tag color="blue">目录</Tag> : null}
              </Space>
            ),
          },
          {
            title: '输入 $/M',
            dataIndex: 'inputPerM',
            align: 'right',
            width: 100,
            render: (v: number) => <span className="price-cell">{fmtPrice(v)}</span>,
          },
          {
            title: '缓存读 $/M',
            dataIndex: 'cachedInputPerM',
            align: 'right',
            width: 110,
            render: (v: number | null, p: ModelPricing) =>
              v != null ? (
                <span className="price-cell">{fmtPrice(v)}</span>
              ) : (
                <Tooltip title="未配置，按输入价回退">
                  <span className="price-cell text-tertiary">{fmtPrice(p.inputPerM)}</span>
                </Tooltip>
              ),
          },
          {
            title: '缓存写 $/M',
            dataIndex: 'cacheWritePerM',
            align: 'right',
            width: 110,
            render: (v: number | null, p: ModelPricing) =>
              v != null ? (
                <span className="price-cell">{fmtPrice(v)}</span>
              ) : (
                <Tooltip title="未配置，按输入价回退">
                  <span className="price-cell text-tertiary">{fmtPrice(p.inputPerM)}</span>
                </Tooltip>
              ),
          },
          {
            title: '输出 $/M',
            dataIndex: 'outputPerM',
            align: 'right',
            width: 100,
            render: (v: number) => <span className="price-cell">{fmtPrice(v)}</span>,
          },
          {
            title: '操作',
            width: 130,
            render: (_, p) => (
              <Space>
                <Button size="small" onClick={() => openEdit(p)}>编辑</Button>
                <Popconfirm
                  title={`删除 ${p.model} 的价目？`}
                  description="删除后该模型的请求费用将记为未定价"
                  onConfirm={async () => {
                    await adminDeletePricing(p.model)
                    message.success('已删除')
                    refresh()
                  }}
                >
                  <Button size="small" danger>删除</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={editing ? `编辑价目：${editing.model}` : '新增模型价目'} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="model" label="模型名" rules={[{ required: true, message: '请输入模型名' }]}>
            <Input disabled={!!editing} placeholder="如 deepseek-chat" />
          </Form.Item>
          <Space size="large">
            <Form.Item name="inputPerM" label="输入 $/M" rules={[{ required: true }]}>
              <InputNumber min={0} step={0.01} style={{ width: 120 }} />
            </Form.Item>
            <Form.Item name="outputPerM" label="输出 $/M" rules={[{ required: true }]}>
              <InputNumber min={0} step={0.01} style={{ width: 120 }} />
            </Form.Item>
          </Space>
          <Space size="large">
            <Form.Item name="cachedInputPerM" label="缓存读 $/M（空=同输入价）">
              <InputNumber min={0} step={0.01} style={{ width: 150 }} />
            </Form.Item>
            <Form.Item name="cacheWritePerM" label="缓存写 $/M（空=同输入价）">
              <InputNumber min={0} step={0.01} style={{ width: 150 }} />
            </Form.Item>
          </Space>
        </Form>
      </Modal>
      <Modal title="导入价目 JSON" open={importOpen} onOk={runImport} onCancel={() => setImportOpen(false)} width={560}>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          粘贴 JSON 数组，字段：model、inputPerM、outputPerM、cachedInputPerM?、cacheWritePerM?（与导出格式一致，同 model 覆盖）
        </Typography.Paragraph>
        <Input.TextArea rows={10} value={importText} onChange={(e) => setImportText(e.target.value)} placeholder='[{"model":"deepseek-chat","inputPerM":0.27,"outputPerM":1.1}]' />
      </Modal>
    </div>
  )
}

const ProxiesTab: React.FC = () => {
  const [proxies, setProxies] = React.useState<Proxy[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<Proxy | null>(null)
  const [form] = Form.useForm()
  const refresh = React.useCallback(() => {
    setLoading(true)
    adminProxies()
      .then((r) => setProxies(r.proxies))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  React.useEffect(refresh, [refresh])
  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ name: '', url: '', note: '', enabled: true })
    setModalOpen(true)
  }
  const openEdit = (p: Proxy) => {
    setEditing(p)
    form.setFieldsValue({ name: p.name, url: '', note: p.note, enabled: p.enabled })
    setModalOpen(true)
  }
  const submit = async () => {
    const v = await form.validateFields()
    try {
      if (editing) {
        await adminUpdateProxy(editing.id, v)
        message.success('已更新，缓存即时生效')
      } else {
        await adminCreateProxy(v)
        message.success('已创建')
      }
      setModalOpen(false)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }
  return (
    <div>
      <div style={{ marginBottom: 12, textAlign: 'right' }}>
        <Button type="primary" onClick={openCreate}>新建公共代理</Button>
      </div>
      <Table<Proxy>
        rowKey="id"
        loading={loading}
        dataSource={proxies}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '状态', dataIndex: 'enabled', render: (v: boolean) => (v ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>) },
          { title: '备注', dataIndex: 'note' },
          {
            title: '操作',
            width: 130,
            render: (_, p) => (
              <Space>
                <Button size="small" onClick={() => openEdit(p)}>编辑</Button>
                <Popconfirm
                  title={`删除公共代理 ${p.name}？`}
                  description="允许走公共代理的渠道将无法再使用它"
                  onConfirm={async () => {
                    await adminDeleteProxy(p.id)
                    message.success('已删除')
                    refresh()
                  }}
                >
                  <Button size="small" danger>删除</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={editing ? `编辑代理：${editing.name}` : '新建公共代理'} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input placeholder="如 mihomo-出口A" />
          </Form.Item>
          <Form.Item name="url" label={editing ? '代理地址（留空不修改）' : '代理地址'}>
            <Input placeholder="socks5://127.0.0.1:7890 或 http://host:port" />
          </Form.Item>
          <Form.Item name="note" label="备注">
            <Input />
          </Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

const CatalogModelsTab: React.FC = () => {
  const [models, setModels] = React.useState<CatalogModel[]>([])
  const [pricingList, setPricingList] = React.useState<ModelPricing[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<CatalogModel | null>(null)
  const [picked, setPicked] = React.useState<ModelPricing | null>(null)
  const [form] = Form.useForm()
  const refresh = React.useCallback(() => {
    setLoading(true)
    Promise.all([adminCatalogModels(), adminPricing()])
      .then(([m, p]) => {
        setModels(m.models)
        setPricingList(p.pricing)
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  React.useEffect(refresh, [refresh])
  // 价目表候选（新增弹窗搜索点选用）：排除已收录条目，右侧显示价格摘要
  const priceOptions = React.useMemo(() => {
    const existing = new Set(models.map((m) => m.name))
    return pricingList
      .filter((p) => !existing.has(p.model))
      .map((p) => ({
        value: p.model,
        label: (
          <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12 }}>
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{p.model}</span>
            <span className="text-tertiary" style={{ fontSize: 12, whiteSpace: 'nowrap' }}>
              输入 ${fmtPrice(p.inputPerM)} / 输出 ${fmtPrice(p.outputPerM)}
            </span>
          </div>
        ),
      }))
  }, [pricingList, models])
  const openCreate = () => {
    setEditing(null)
    setPicked(null)
    form.resetFields()
    form.setFieldsValue({ name: '', note: '', enabled: true })
    setModalOpen(true)
  }
  const openEdit = (m: CatalogModel) => {
    setEditing(m)
    setPicked(null)
    form.setFieldsValue({ name: m.name, note: m.note, enabled: m.enabled })
    setModalOpen(true)
  }
  const submit = async () => {
    const v = await form.validateFields()
    try {
      if (editing) {
        await adminUpdateCatalogModel(editing.id, { name: v.name, note: v.note, enabled: v.enabled })
        message.success('已更新')
      } else {
        await adminCreateCatalogModel({ name: v.name, note: v.note })
        message.success('已添加')
      }
      setModalOpen(false)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }
  return (
    <div>
      <div style={{ marginBottom: 12, textAlign: 'right' }}>
        <Button type="primary" onClick={openCreate}>新增模型</Button>
      </div>
      <div className="tab-note">
        模型目录是全局点选数据源：用户在渠道表单与模板表单中从这里点选模型；删除目录项不影响已引用它的渠道配置。
        只收录用户常用的模型即可，无需与价目表对齐；新增时可从价目表搜索点选，价格自动关联。
      </div>
      <Table<CatalogModel>
        rowKey="id"
        loading={loading}
        dataSource={models}
        pagination={{ pageSize: 20 }}
        columns={[
          { title: '模型', dataIndex: 'name' },
          {
            title: '单价（$/百万 tokens）',
            width: 230,
            render: (_: unknown, m: CatalogModel) => <CatalogPrice m={m} />,
          },
          { title: '备注', dataIndex: 'note', ellipsis: true, render: (v: string) => v || '-' },
          {
            title: '状态',
            dataIndex: 'enabled',
            width: 90,
            render: (v: boolean) => (v ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>),
          },
          {
            title: '操作',
            width: 130,
            render: (_, m) => (
              <Space>
                <Button size="small" onClick={() => openEdit(m)}>编辑</Button>
                <Popconfirm
                  title={`从目录移除 ${m.name}？`}
                  description="已引用它的渠道配置不受影响"
                  onConfirm={async () => {
                    await adminDeleteCatalogModel(m.id)
                    message.success('已删除')
                    refresh()
                  }}
                >
                  <Button size="small" danger>删除</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={editing ? `编辑目录模型：${editing.name}` : '新增目录模型'} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item
            name="name"
            label="模型名"
            rules={[{ required: true, message: '请输入模型名' }]}
            extra={
              picked ? (
                <span>
                  价目关联：输入 ${fmtPrice(picked.inputPerM)} · 输出 ${fmtPrice(picked.outputPerM)} ·
                  缓存读 ${fmtPrice(picked.cachedInputPerM ?? picked.inputPerM)} ·
                  缓存写 ${fmtPrice(picked.cacheWritePerM ?? picked.inputPerM)}
                </span>
              ) : (
                <span className="form-hint">可从价目表搜索点选，也可直接输入目录外名称（费用将记为未定价）</span>
              )
            }
          >
            {editing ? (
              <Input placeholder="如 claude-sonnet-4.5" />
            ) : (
              <AutoComplete
                allowClear
                options={priceOptions}
                onClear={() => setPicked(null)}
                onSelect={(v: string) => setPicked(pricingList.find((p) => p.model === v) ?? null)}
                onChange={(v: string) => {
                  if (picked && picked.model !== v) setPicked(null)
                }}
                filterOption={(input, option) =>
                  String(option?.value ?? '').toLowerCase().includes(input.trim().toLowerCase())
                }
                placeholder="输入关键字从价目表搜索选择，如 claude-sonnet"
              />
            )}
          </Form.Item>
          <Form.Item name="note" label="备注">
            <Input.TextArea rows={2} placeholder="如 2026-09 在售，适合 Agent 主力" />
          </Form.Item>
          {editing ? (
            <Form.Item name="enabled" label="启用（停用后不再出现在用户点选项中）" valuePropName="checked">
              <Switch />
            </Form.Item>
          ) : null}
        </Form>
      </Modal>
    </div>
  )
}

const TemplatesTab: React.FC = () => {
  const [templates, setTemplates] = React.useState<ChannelTemplate[]>([])
  const [catalog, setCatalog] = React.useState<CatalogModel[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<ChannelTemplate | null>(null)
  const [form] = Form.useForm()
  const refresh = React.useCallback(() => {
    setLoading(true)
    Promise.all([adminTemplates(), adminCatalogModels()])
      .then(([t, m]) => {
        setTemplates(t.templates)
        setCatalog(m.models)
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  React.useEffect(refresh, [refresh])
  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ baseUrls: [''], models: [], lineStrategy: 'auto', priorityDefault: 0, allowPublicProxyDefault: false, note: '', enabled: true })
    setModalOpen(true)
  }
  const openEdit = (t: ChannelTemplate) => {
    setEditing(t)
    form.setFieldsValue({
      name: t.name, baseUrls: t.baseUrls, lineStrategy: t.lineStrategy,
      models: t.models, modelMapping: Object.entries(t.modelMapping).map(([from, to]) => ({ from, to })),
      priorityDefault: t.priorityDefault, allowPublicProxyDefault: t.allowPublicProxyDefault,
      note: t.note, enabled: t.enabled,
    })
    setModalOpen(true)
  }
  const submit = async () => {
    const v = await form.validateFields()
    const mapping: Record<string, string> = {}
    for (const { from, to } of v.modelMapping || []) {
      if (from && to) mapping[from] = to
    }
    const input = {
      name: v.name, baseUrls: (v.baseUrls as string[]).filter(Boolean),
      lineStrategy: v.lineStrategy, models: v.models || [], modelMapping: mapping,
      priorityDefault: v.priorityDefault, allowPublicProxyDefault: v.allowPublicProxyDefault,
      note: v.note, enabled: v.enabled,
    }
    try {
      if (editing) {
        await adminUpdateTemplate(editing.id, input)
        message.success('已更新')
      } else {
        await adminCreateTemplate(input)
        message.success('已创建')
      }
      setModalOpen(false)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }
  return (
    <div>
      <div style={{ marginBottom: 12, textAlign: 'right' }}>
        <Button type="primary" onClick={openCreate}>新建模板</Button>
      </div>
      <Table<ChannelTemplate>
        rowKey="id"
        loading={loading}
        dataSource={templates}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '线路数', width: 90, align: 'right', render: (_, t) => t.baseUrls.length },
          { title: '模型数', width: 90, align: 'right', render: (_, t) => t.models.length },
          { title: '复制次数', dataIndex: 'copyCount', width: 90, align: 'right' },
          { title: '说明', dataIndex: 'note', ellipsis: true },
          {
            title: '状态',
            dataIndex: 'enabled',
            render: (v: boolean) => (v ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>),
          },
          {
            title: '操作',
            width: 130,
            render: (_, t) => (
              <Space>
                <Button size="small" onClick={() => openEdit(t)}>编辑</Button>
                <Popconfirm
                  title={`删除模板 ${t.name}？`}
                  description="已复制的渠道不受影响"
                  onConfirm={async () => {
                    await adminDeleteTemplate(t.id)
                    message.success('已删除（已复制渠道不受影响）')
                    refresh()
                  }}
                >
                  <Button size="small" danger>删除</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={editing ? `编辑模板：${editing.name}` : '新建预制模板'} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} width={640} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input />
          </Form.Item>
          <Form.Item label="线路（base_url，按优先顺序，≤5 条）" required>
            <Form.List name="baseUrls">
              {(fields, { add, remove }) => (
                <>
                  {fields.map((f) => (
                    <Space key={f.key} style={{ display: 'flex', marginBottom: 4 }}>
                      <Form.Item name={[f.name]} noStyle rules={[{ required: true, message: '线路不能为空' }]}>
                        <Input placeholder="https://api.example.com" style={{ width: 400 }} />
                      </Form.Item>
                      {fields.length > 1 ? <a onClick={() => remove(f.name)}>删除</a> : null}
                    </Space>
                  ))}
                  {fields.length < 5 ? (
                    <Button type="dashed" onClick={() => add('')} block>添加线路</Button>
                  ) : null}
                </>
              )}
            </Form.List>
          </Form.Item>
          <Form.Item name="models" label="模型列表" rules={[{ required: true, message: '至少一个模型' }]} extra="从模型目录点选；目录外的名称可直接输入回车添加。">
            <Select mode="tags" tokenSeparators={[',']} options={catalog.map((m) => ({ value: m.name }))} placeholder="点选或输入模型名" />
          </Form.Item>
          <Space size="large">
            <Form.Item name="lineStrategy" label="线路策略">
              <Select
                style={{ width: 150 }}
                options={[
                  { value: 'auto', label: 'auto（探测优选）' },
                  { value: 'manual', label: 'manual（固定第一条）' },
                ]}
              />
            </Form.Item>
            <Form.Item name="priorityDefault" label="默认优先级">
              <InputNumber />
            </Form.Item>
            <Form.Item name="allowPublicProxyDefault" label="默认允许公共代理" valuePropName="checked">
              <Switch />
            </Form.Item>
          </Space>
          <Form.Item label="模型映射（请求名 → 上游名）">
            <Form.List name="modelMapping">
              {(fields, { add, remove }) => (
                <>
                  {fields.map((f) => (
                    <Space key={f.key} style={{ display: 'flex', marginBottom: 4 }}>
                      <Form.Item name={[f.name, 'from']} noStyle>
                        <Input placeholder="请求模型名" style={{ width: 180 }} />
                      </Form.Item>
                      <span>→</span>
                      <Form.Item name={[f.name, 'to']} noStyle>
                        <Input placeholder="上游模型名" style={{ width: 180 }} />
                      </Form.Item>
                      <a onClick={() => remove(f.name)}>删除</a>
                    </Space>
                  ))}
                  <Button type="dashed" onClick={() => add({ from: '', to: '' })} block>添加映射</Button>
                </>
              )}
            </Form.List>
          </Form.Item>
          <Form.Item name="note" label="说明">
            <Input.TextArea rows={2} />
          </Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

const StatsTab: React.FC = () => {
  const [data, setData] = React.useState<StatsResponse | null>(null)
  const [loading, setLoading] = React.useState(true)
  React.useEffect(() => {
    adminStats({ days: 30 })
      .then(setData)
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  return (
    <Table<StatsGroup>
      rowKey="dim"
      loading={loading}
      dataSource={data?.byModel ?? []}
      columns={[
        { title: '模型', dataIndex: 'dim' },
        { title: '请求数', dataIndex: 'requests', align: 'right', render: (v: number) => fmtInt(v) },
        { title: '输入 tokens', dataIndex: 'promptTokens', align: 'right', render: (v: number) => fmtInt(v) },
        { title: '输出 tokens', dataIndex: 'completionTokens', align: 'right', render: (v: number) => fmtInt(v) },
        { title: '费用估算', dataIndex: 'cost', align: 'right', render: (v: number) => fmtCost(v) },
      ]}
    />
  )
}

const SettingsTab: React.FC = () => {
  const [form] = Form.useForm()
  const [loading, setLoading] = React.useState(true)
  const [saving, setSaving] = React.useState(false)
  const [syncing, setSyncing] = React.useState(false)
  const [hasSecret, setHasSecret] = React.useState(false)
  const [fxMode, setFxMode] = React.useState<'auto' | 'manual'>('manual')
  const [fxInfo, setFxInfo] = React.useState<{ rate?: number; source?: string; sourceUrl?: string; updatedAt?: string }>({})
  React.useEffect(() => {
    adminSettings()
      .then((r) => {
        form.setFieldsValue({
          registerMode: r.settings.registerMode,
          feishuEnabled: r.settings.feishuEnabled,
          feishuAppId: r.settings.feishuAppId,
          feishuBaseUrl: r.settings.feishuBaseUrl,
          exchangeRate: r.settings.exchangeRate ?? 7.2,
          exchangeRateMode: r.settings.exchangeRateMode ?? 'manual',
        })
        setFxMode(r.settings.exchangeRateMode ?? 'manual')
        setFxInfo({
          rate: r.settings.exchangeRate,
          source: r.settings.exchangeRateSource,
          sourceUrl: r.settings.exchangeRateSourceUrl,
          updatedAt: r.settings.exchangeRateUpdatedAt,
        })
        setHasSecret(r.settings.feishuHasSecret ?? false)
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [form])

  // doSync 立即同步汇率：apply=true 写入生效；apply=false 仅获取最新值填充表单
  const doSync = async (apply: boolean) => {
    setSyncing(true)
    try {
      const r = await adminSyncExchangeRate(apply)
      const res = r.result
      if (apply) {
        setFxInfo((prev) => ({ rate: res.rate, source: res.source, sourceUrl: res.sourceUrl, updatedAt: res.updatedAt ?? prev.updatedAt }))
        message.success(`已同步：1 USD = ${res.rate} CNY（来源 ${res.source}）`)
      } else {
        form.setFieldValue('exchangeRate', res.rate)
        message.success(`已获取最新汇率 ${res.rate}，确认后保存`)
      }
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSyncing(false)
    }
  }

  return (
    <Form
      form={form}
      layout="vertical"
      style={{ maxWidth: 520 }}
      onFinish={async (v) => {
        setSaving(true)
        try {
          await adminUpdateSettings(v as AdminSettings)
          // 切到自动同步后立即拉一次，避免等到下个周期
          if (v.exchangeRateMode === 'auto') await doSync(true)
          else message.success('已保存')
        } catch (e) {
          message.error((e as Error).message)
        } finally {
          setSaving(false)
        }
      }}
    >
      <Form.Item name="registerMode" label="注册策略">
        <Select
          options={[
            { value: 'open', label: '开放注册' },
            { value: 'invite', label: '邀请码制' },
            { value: 'closed', label: '关闭注册' },
          ]}
        />
      </Form.Item>
      <Form.Item name="feishuEnabled" label="飞书登录" valuePropName="checked">
        <Switch />
      </Form.Item>
      <Form.Item name="feishuAppId" label="飞书 App ID">
        <Input placeholder="cli_xxxx" />
      </Form.Item>
      <Form.Item name="feishuAppSecret" label={`飞书 App Secret${hasSecret ? '（已配置，留空不修改）' : ''}`}>
        <Input.Password placeholder="仅保存时提交，不回显" />
      </Form.Item>
      <Form.Item name="feishuBaseUrl" label="飞书开放平台地址（默认官方，测试可覆盖）">
        <Input placeholder="https://open.feishu.cn" />
      </Form.Item>
      <Form.Item
        name="exchangeRateMode"
        label="美元兑人民币汇率"
        extra="人民币渠道（cny_ratio）的费用折算用；自动同步每 24 小时从公共汇率源拉取"
      >
        <Radio.Group
          onChange={(e) => setFxMode(e.target.value)}
          options={[
            { value: 'auto', label: '自动同步' },
            { value: 'manual', label: '固定值' },
          ]}
        />
      </Form.Item>
      {fxMode === 'auto' ? (
        <Form.Item label="当前汇率">
          <Space wrap>
            <span>
              1 USD = <Typography.Text strong>{fxInfo.rate ?? 7.2}</Typography.Text> CNY
            </span>
            {fxInfo.source && <Tag>{fxInfo.source}</Tag>}
            <Typography.Text type="secondary">更新于 {formatDateTime(fxInfo.updatedAt)}</Typography.Text>
            <Button size="small" loading={syncing} onClick={() => doSync(true)}>
              立即同步
            </Button>
          </Space>
          {fxInfo.sourceUrl && (
            <div>
              <Typography.Link href={fxInfo.sourceUrl} target="_blank" rel="noreferrer" style={{ fontSize: 12 }}>
                {fxInfo.sourceUrl}
              </Typography.Link>
            </div>
          )}
        </Form.Item>
      ) : (
        <Form.Item label="固定值" required>
          <Space>
            <Form.Item name="exchangeRate" noStyle rules={[{ required: true, message: '请输入汇率固定值' }]}>
              <InputNumber min={0.5} max={20} step={0.1} style={{ width: 160 }} />
            </Form.Item>
            <Button size="small" loading={syncing} onClick={() => doSync(false)}>
              获取最新
            </Button>
          </Space>
        </Form.Item>
      )}
      <Button type="primary" htmlType="submit" loading={loading || saving}>
        保存
      </Button>
    </Form>
  )
}

const AdminPage: React.FC = () => (
  <div>
    <div className="page-heading">
      <div><h2>管理</h2><p>系统级配置：用量、用户、模型目录、价目、代理与模板。</p></div>
    </div>
    <Tabs
      items={[
        { key: 'stats', label: '用量/花费（30 天）', children: <StatsTab /> },
        { key: 'users', label: '用户', children: <UsersTab /> },
        { key: 'models', label: '模型目录', children: <CatalogModelsTab /> },
        { key: 'pricing', label: '模型价目表', children: <PricingTab /> },
        { key: 'proxies', label: '公共代理池', children: <ProxiesTab /> },
        { key: 'templates', label: '预制模板', children: <TemplatesTab /> },
        { key: 'settings', label: '系统设置', children: <SettingsTab /> },
      ]}
    />
  </div>
)

export default AdminPage
