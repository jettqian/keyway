import React from 'react'
import { Tabs, Table, Form, Switch, Input, Button, message, Modal, Popconfirm, Tag, Space, Select, InputNumber, Typography, Radio, Tooltip, AutoComplete, Card, DatePicker, Statistic } from 'antd'
import { SearchOutlined, ReloadOutlined, DownloadOutlined } from '@ant-design/icons'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
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
import { formatDateTime, fmtInt, fmtTokens } from '../format'
import { useI18n } from '../i18n'
import CatalogPrice from '../components/CatalogPrice'
import Money from '../components/Money'
import ModelTags from '../components/ModelTags'
import { rangePresets } from './Stats'

const UsersTab: React.FC = () => {
  const { t } = useI18n()
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
      scroll={{ x: 'max-content' }}
      columns={[
        { title: 'ID', dataIndex: 'id', width: 60 },
        { title: t('admin.username'), dataIndex: 'username' },
        { title: t('common.role'), dataIndex: 'role', render: (r: number) => (r === 100 ? <Tag color="red">{t('admin.adminRole')}</Tag> : <Tag>{t('common.user')}</Tag>) },
        {
          title: t('common.status'),
          dataIndex: 'status',
          render: (s: number) => (s === 1 ? <Tag color="green">{t('admin.statusNormal')}</Tag> : <Tag color="orange">{t('admin.statusDisabled')}</Tag>),
        },
        { title: t('admin.registeredAt'), dataIndex: 'createdAt', width: 180, render: (v: number | string) => formatDateTime(v) },
        { title: t('admin.lastLoginAt'), dataIndex: 'lastLoginAt', width: 180, render: (v: number | string) => formatDateTime(v) },
        {
          title: t('common.action'),
          render: (_, u) => (
            <Space>
              <Button
                size="small"
                onClick={async () => {
                  await adminSetUserStatus(u.id, u.status === 1 ? 2 : 1)
                  message.success(t('common.updated'))
                  refresh()
                }}
              >
                {u.status === 1 ? t('admin.disableUser') : t('common.enabled')}
              </Button>
              <Button
                size="small"
                onClick={async () => {
                  const r = await adminResetPassword(u.id)
                  Modal.info({ title: t('admin.newPassword'), content: <Typography.Text copyable>{r.password}</Typography.Text> })
                }}
              >
                {t('admin.resetPassword')}
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
  const { t } = useI18n()
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
      message.success(editing ? t('admin.pricingUpdated') : t('common.added'))
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
      message.error(t('admin.invalidJsonArray'))
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
    message.success(t('admin.importResult', { ok, total: list.length }))
    setImportOpen(false)
    refresh()
  }
  const [syncing, setSyncing] = React.useState(false)
  const syncRemote = async () => {
    setSyncing(true)
    try {
      const r = await adminSyncRemotePricing()
      const { litellmAdded, openrouterAdded, skippedExisting, warnings } = r.result
      const sep = t('admin.listSeparator')
      const parts = [t('admin.syncAdded', { litellm: litellmAdded, openrouter: openrouterAdded })]
      if (skippedExisting) parts.push(t('admin.syncSkipped', { count: skippedExisting }))
      const head = parts.join(sep)
      message.success(warnings?.length ? head + t('admin.warningsSuffix', { warnings: warnings.join(sep) }) : head)
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
              t('admin.lastSyncedAt', { time: formatDateTime(syncedAt) })
            ) : (
              t('admin.notSyncedYet')
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
            title={t('admin.syncConfirmTitle')}
            description={t('admin.syncConfirmDesc')}
            onConfirm={syncRemote}
          >
            <Button loading={syncing}>{t('admin.syncOfficialPricing')}</Button>
          </Popconfirm>
          <Button onClick={exportJSON}>{t('admin.exportJson')}</Button>
          <Button onClick={() => { setImportText(''); setImportOpen(true) }}>{t('admin.importJson')}</Button>
          <Button type="primary" onClick={openCreate}>{t('admin.addModel')}</Button>
        </Space>
      </div>
      <div style={{ marginBottom: 12 }}>
        <Input
          allowClear
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t('admin.searchModelPlaceholder')}
          style={{ width: 240 }}
          prefix={<SearchOutlined />}
        />
        <span className="text-tertiary" style={{ fontSize: 12, marginLeft: 12 }}>
          {t('admin.totalEntries', { count: pricing.length })}
          {search.trim() ? t('admin.matchedEntries', { count: rows.length }) : ''}
          {t('admin.catalogFirstNote')}
        </span>
      </div>
      <Table<ModelPricing>
        rowKey="model"
        loading={loading}
        dataSource={rows}
        scroll={{ x: 'max-content' }}
        pagination={{ pageSize: 20 }}
        columns={[
          {
            title: t('common.model'),
            dataIndex: 'model',
            render: (n: string) => (
              <Space>
                {n}
                {catalogNames.has(n) ? <Tag color="blue">{t('admin.catalogTag')}</Tag> : null}
              </Space>
            ),
          },
          {
            title: t('admin.inputPerM'),
            dataIndex: 'inputPerM',
            align: 'right',
            width: 100,
            render: (v: number) => <Money value={v} />,
          },
          {
            title: t('admin.cacheReadPerM'),
            dataIndex: 'cachedInputPerM',
            align: 'right',
            width: 110,
            render: (v: number | null, p: ModelPricing) =>
              v != null ? (
                <Money value={v} />
              ) : (
                <Tooltip title={t('admin.fallbackToInputPrice')}>
                  <span className="text-tertiary"><Money value={p.inputPerM} /></span>
                </Tooltip>
              ),
          },
          {
            title: t('admin.cacheWritePerM'),
            dataIndex: 'cacheWritePerM',
            align: 'right',
            width: 110,
            render: (v: number | null, p: ModelPricing) =>
              v != null ? (
                <Money value={v} />
              ) : (
                <Tooltip title={t('admin.fallbackToInputPrice')}>
                  <span className="text-tertiary"><Money value={p.inputPerM} /></span>
                </Tooltip>
              ),
          },
          {
            title: t('admin.outputPerM'),
            dataIndex: 'outputPerM',
            align: 'right',
            width: 100,
            render: (v: number) => <Money value={v} />,
          },
          {
            title: t('common.action'),
            width: 130,
            render: (_, p) => (
              <Space>
                <Button size="small" onClick={() => openEdit(p)}>{t('common.edit')}</Button>
                <Popconfirm
                  title={t('admin.deletePricingConfirm', { model: p.model })}
                  description={t('admin.deletePricingDesc')}
                  onConfirm={async () => {
                    await adminDeletePricing(p.model)
                    message.success(t('common.deleted'))
                    refresh()
                  }}
                >
                  <Button size="small" danger>{t('common.delete')}</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={editing ? t('admin.editPricingTitle', { model: editing.model }) : t('admin.addPricingTitle')} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="model" label={t('admin.modelName')} rules={[{ required: true, message: t('admin.modelNameRequired') }]}>
            <Input disabled={!!editing} placeholder={t('admin.modelNamePlaceholder')} />
          </Form.Item>
          <Space size="large">
            <Form.Item name="inputPerM" label={t('admin.inputPerM')} rules={[{ required: true }]}>
              <InputNumber min={0} step={0.01} style={{ width: 120 }} />
            </Form.Item>
            <Form.Item name="outputPerM" label={t('admin.outputPerM')} rules={[{ required: true }]}>
              <InputNumber min={0} step={0.01} style={{ width: 120 }} />
            </Form.Item>
          </Space>
          <Space size="large">
            <Form.Item name="cachedInputPerM" label={t('admin.cacheReadPerMOptional')}>
              <InputNumber min={0} step={0.01} style={{ width: 150 }} />
            </Form.Item>
            <Form.Item name="cacheWritePerM" label={t('admin.cacheWritePerMOptional')}>
              <InputNumber min={0} step={0.01} style={{ width: 150 }} />
            </Form.Item>
          </Space>
        </Form>
      </Modal>
      <Modal title={t('admin.importPricingTitle')} open={importOpen} onOk={runImport} onCancel={() => setImportOpen(false)} width={560}>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          {t('admin.importPricingHint')}
        </Typography.Paragraph>
        <Input.TextArea rows={10} value={importText} onChange={(e) => setImportText(e.target.value)} placeholder='[{"model":"deepseek-chat","inputPerM":0.27,"outputPerM":1.1}]' />
      </Modal>
    </div>
  )
}

const ProxiesTab: React.FC = () => {
  const { t } = useI18n()
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
        message.success(t('admin.proxyUpdated'))
      } else {
        await adminCreateProxy(v)
        message.success(t('common.created'))
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
        <Button type="primary" onClick={openCreate}>{t('admin.createProxy')}</Button>
      </div>
      <Table<Proxy>
        rowKey="id"
        loading={loading}
        dataSource={proxies}
        scroll={{ x: 'max-content' }}
        columns={[
          { title: t('common.name'), dataIndex: 'name' },
          { title: t('common.status'), dataIndex: 'enabled', render: (v: boolean) => (v ? <Tag color="green">{t('common.enabled')}</Tag> : <Tag>{t('common.disabled')}</Tag>) },
          { title: t('common.note'), dataIndex: 'note' },
          {
            title: t('common.action'),
            width: 130,
            render: (_, p) => (
              <Space>
                <Button size="small" onClick={() => openEdit(p)}>{t('common.edit')}</Button>
                <Popconfirm
                  title={t('admin.deleteProxyConfirm', { name: p.name })}
                  description={t('admin.deleteProxyDesc')}
                  onConfirm={async () => {
                    await adminDeleteProxy(p.id)
                    message.success(t('common.deleted'))
                    refresh()
                  }}
                >
                  <Button size="small" danger>{t('common.delete')}</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={editing ? t('admin.editProxyTitle', { name: editing.name }) : t('admin.createProxy')} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('common.nameRequired') }]}>
            <Input placeholder={t('admin.proxyNamePlaceholder')} />
          </Form.Item>
          <Form.Item name="url" label={editing ? t('admin.proxyUrlKeep') : t('admin.proxyUrl')}>
            <Input placeholder={t('admin.proxyUrlPlaceholder')} />
          </Form.Item>
          <Form.Item name="note" label={t('common.note')}>
            <Input />
          </Form.Item>
          <Form.Item name="enabled" label={t('common.enabled')} valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

const CatalogModelsTab: React.FC = () => {
  const { t } = useI18n()
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
              {t('common.input')} <Money value={p.inputPerM} /> / {t('common.output')} <Money value={p.outputPerM} />
            </span>
          </div>
        ),
      }))
  }, [pricingList, models, t])
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
        message.success(t('common.updated'))
      } else {
        await adminCreateCatalogModel({ name: v.name, note: v.note })
        message.success(t('common.added'))
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
        <Button type="primary" onClick={openCreate}>{t('admin.addModel')}</Button>
      </div>
      <div className="tab-note">
        {t('admin.catalogNote1')} {t('admin.catalogNote2')}
      </div>
      <Table<CatalogModel>
        rowKey="id"
        loading={loading}
        dataSource={models}
        scroll={{ x: 'max-content' }}
        pagination={{ pageSize: 20 }}
        columns={[
          { title: t('common.model'), dataIndex: 'name' },
          {
            title: t('admin.unitPrice'),
            width: 230,
            render: (_: unknown, m: CatalogModel) => <CatalogPrice m={m} />,
          },
          { title: t('common.note'), dataIndex: 'note', ellipsis: true, render: (v: string) => v || '-' },
          {
            title: t('common.status'),
            dataIndex: 'enabled',
            width: 90,
            render: (v: boolean) => (v ? <Tag color="green">{t('common.enabled')}</Tag> : <Tag>{t('common.disabled')}</Tag>),
          },
          {
            title: t('common.action'),
            width: 130,
            render: (_, m) => (
              <Space>
                <Button size="small" onClick={() => openEdit(m)}>{t('common.edit')}</Button>
                <Popconfirm
                  title={t('admin.removeCatalogConfirm', { name: m.name })}
                  description={t('admin.removeCatalogDesc')}
                  onConfirm={async () => {
                    await adminDeleteCatalogModel(m.id)
                    message.success(t('common.deleted'))
                    refresh()
                  }}
                >
                  <Button size="small" danger>{t('common.delete')}</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={editing ? t('admin.editCatalogTitle', { name: editing.name }) : t('admin.addCatalogTitle')} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item
            name="name"
            label={t('admin.modelName')}
            rules={[{ required: true, message: t('admin.modelNameRequired') }]}
            extra={
              picked ? (
                <span>
                  {t('admin.priceLinked')}{t('common.input')} <Money value={picked.inputPerM} /> · {t('common.output')} <Money value={picked.outputPerM} /> ·
                  {t('common.cacheRead')} <Money value={picked.cachedInputPerM ?? picked.inputPerM} /> ·
                  {t('common.cacheWrite')} <Money value={picked.cacheWritePerM ?? picked.inputPerM} />
                </span>
              ) : (
                <span className="form-hint">{t('admin.catalogNameHint')}</span>
              )
            }
          >
            {editing ? (
              <Input placeholder={t('admin.catalogEditPlaceholder')} />
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
                placeholder={t('admin.catalogPickPlaceholder')}
              />
            )}
          </Form.Item>
          <Form.Item name="note" label={t('common.note')}>
            <Input.TextArea rows={2} placeholder={t('admin.catalogNotePlaceholder')} />
          </Form.Item>
          {editing ? (
            <Form.Item name="enabled" label={t('admin.catalogEnabledLabel')} valuePropName="checked">
              <Switch />
            </Form.Item>
          ) : null}
        </Form>
      </Modal>
    </div>
  )
}

const TemplatesTab: React.FC = () => {
  const { t } = useI18n()
  const [templates, setTemplates] = React.useState<ChannelTemplate[]>([])
  const [catalog, setCatalog] = React.useState<CatalogModel[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<ChannelTemplate | null>(null)
  const [form] = Form.useForm()
  const refresh = React.useCallback(() => {
    setLoading(true)
    Promise.all([adminTemplates(), adminCatalogModels()])
      .then(([tpl, m]) => {
        setTemplates(tpl.templates)
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
  const openEdit = (tpl: ChannelTemplate) => {
    setEditing(tpl)
    form.setFieldsValue({
      name: tpl.name, baseUrls: tpl.baseUrls, lineStrategy: tpl.lineStrategy,
      models: tpl.models, modelMapping: Object.entries(tpl.modelMapping).map(([from, to]) => ({ from, to })),
      priorityDefault: tpl.priorityDefault, allowPublicProxyDefault: tpl.allowPublicProxyDefault,
      note: tpl.note, enabled: tpl.enabled,
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
        message.success(t('common.updated'))
      } else {
        await adminCreateTemplate(input)
        message.success(t('common.created'))
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
        <Button type="primary" onClick={openCreate}>{t('admin.createTemplate')}</Button>
      </div>
      <Table<ChannelTemplate>
        rowKey="id"
        loading={loading}
        dataSource={templates}
        scroll={{ x: 'max-content' }}
        columns={[
          { title: t('common.name'), dataIndex: 'name' },
          { title: t('admin.lineCount'), width: 90, align: 'right', render: (_, tpl) => tpl.baseUrls.length },
          { title: t('common.model'), render: (_, tpl) => <ModelTags models={tpl.models} /> },
          { title: t('admin.copyCount'), dataIndex: 'copyCount', width: 90, align: 'right' },
          { title: t('common.description'), dataIndex: 'note', ellipsis: true },
          {
            title: t('common.status'),
            dataIndex: 'enabled',
            render: (v: boolean) => (v ? <Tag color="green">{t('common.enabled')}</Tag> : <Tag>{t('common.disabled')}</Tag>),
          },
          {
            title: t('common.action'),
            width: 130,
            render: (_, tpl) => (
              <Space>
                <Button size="small" onClick={() => openEdit(tpl)}>{t('common.edit')}</Button>
                <Popconfirm
                  title={t('admin.deleteTemplateConfirm', { name: tpl.name })}
                  description={t('admin.deleteTemplateDesc')}
                  onConfirm={async () => {
                    await adminDeleteTemplate(tpl.id)
                    message.success(t('admin.templateDeleted'))
                    refresh()
                  }}
                >
                  <Button size="small" danger>{t('common.delete')}</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={editing ? t('admin.editTemplateTitle', { name: editing.name }) : t('admin.createTemplateTitle')} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} width={640} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('common.nameRequired') }]}>
            <Input />
          </Form.Item>
          <Form.Item label={t('admin.baseUrlsLabel')} required>
            <Form.List name="baseUrls">
              {(fields, { add, remove }) => (
                <>
                  {fields.map((f) => (
                    <Space key={f.key} style={{ display: 'flex', marginBottom: 4 }}>
                      <Form.Item name={[f.name]} noStyle rules={[{ required: true, message: t('admin.baseUrlRequired') }]}>
                        <Input placeholder="https://api.example.com" style={{ width: 400 }} />
                      </Form.Item>
                      {fields.length > 1 ? <a onClick={() => remove(f.name)}>{t('common.delete')}</a> : null}
                    </Space>
                  ))}
                  {fields.length < 5 ? (
                    <Button type="dashed" onClick={() => add('')} block>{t('admin.addLine')}</Button>
                  ) : null}
                </>
              )}
            </Form.List>
          </Form.Item>
          <Form.Item name="models" label={t('admin.modelList')} rules={[{ required: true, message: t('admin.modelListRequired') }]} extra={t('admin.modelListExtra')}>
            <Select mode="tags" tokenSeparators={[',']} options={catalog.map((m) => ({ value: m.name }))} placeholder={t('admin.modelListPlaceholder')} />
          </Form.Item>
          <Space size="large">
            <Form.Item name="lineStrategy" label={t('admin.lineStrategy')}>
              <Select
                style={{ width: 150 }}
                options={[
                  { value: 'auto', label: t('admin.lineStrategyAuto') },
                  { value: 'manual', label: t('admin.lineStrategyManual') },
                ]}
              />
            </Form.Item>
            <Form.Item name="priorityDefault" label={t('admin.priorityDefault')}>
              <InputNumber />
            </Form.Item>
            <Form.Item name="allowPublicProxyDefault" label={t('admin.allowPublicProxyDefault')} valuePropName="checked">
              <Switch />
            </Form.Item>
          </Space>
          <Form.Item label={t('admin.modelMappingLabel')}>
            <Form.List name="modelMapping">
              {(fields, { add, remove }) => (
                <>
                  {fields.map((f) => (
                    <Space key={f.key} style={{ display: 'flex', marginBottom: 4 }}>
                      <Form.Item name={[f.name, 'from']} noStyle>
                        <Input placeholder={t('admin.mappingFromPlaceholder')} style={{ width: 180 }} />
                      </Form.Item>
                      <span>→</span>
                      <Form.Item name={[f.name, 'to']} noStyle>
                        <Input placeholder={t('admin.mappingToPlaceholder')} style={{ width: 180 }} />
                      </Form.Item>
                      <a onClick={() => remove(f.name)}>{t('common.delete')}</a>
                    </Space>
                  ))}
                  <Button type="dashed" onClick={() => add({ from: '', to: '' })} block>{t('admin.addMapping')}</Button>
                </>
              )}
            </Form.List>
          </Form.Item>
          <Form.Item name="note" label={t('common.description')}>
            <Input.TextArea rows={2} />
          </Form.Item>
          <Form.Item name="enabled" label={t('common.enabled')} valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

const StatsTab: React.FC = () => {
  const { t } = useI18n()
  const [range, setRange] = React.useState<[Dayjs, Dayjs]>([dayjs().subtract(6, 'day'), dayjs()])
  const [data, setData] = React.useState<StatsResponse | null>(null)
  const [loading, setLoading] = React.useState(true)

  const refresh = React.useCallback(() => {
    setLoading(true)
    adminStats({ start: range[0].format('YYYY-MM-DD'), end: range[1].format('YYYY-MM-DD') })
      .then(setData)
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [range])

  React.useEffect(refresh, [refresh])

  const groupColumns = (dimName: string) => [
    { title: dimName, dataIndex: 'dim' },
    { title: t('admin.requestCount'), dataIndex: 'requests', align: 'right' as const, render: (v: number) => fmtInt(v), sorter: (a: StatsGroup, b: StatsGroup) => a.requests - b.requests },
    { title: t('admin.inputTokens'), dataIndex: 'promptTokens', align: 'right' as const, render: (v: number) => fmtInt(v), sorter: (a: StatsGroup, b: StatsGroup) => a.promptTokens - b.promptTokens },
    { title: t('admin.outputTokens'), dataIndex: 'completionTokens', align: 'right' as const, render: (v: number) => fmtInt(v), sorter: (a: StatsGroup, b: StatsGroup) => a.completionTokens - b.completionTokens },
    { title: t('admin.costEstimate'), dataIndex: 'cost', align: 'right' as const, render: (v: number) => <Money value={v} mode="cost" />, sorter: (a: StatsGroup, b: StatsGroup) => a.cost - b.cost },
  ]

  const exportUrl = `/api/admin/stats/export?start=${range[0].format('YYYY-MM-DD')}&end=${range[1].format('YYYY-MM-DD')}`

  return (
    <div>
      <div style={{ marginBottom: 16, display: 'flex', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
        <DatePicker.RangePicker
          value={range}
          onChange={(v) => {
            if (v && v[0] && v[1]) setRange([v[0], v[1]])
          }}
          disabledDate={(d) => d.isAfter(dayjs(), 'day')}
          presets={rangePresets}
          allowClear={false}
        />
        <Space>
          <Button icon={<ReloadOutlined />} onClick={refresh} loading={loading}>{t('admin.refresh')}</Button>
          <Button icon={<DownloadOutlined />} href={exportUrl}>{t('admin.exportCsv')}</Button>
        </Space>
      </div>
      <Card loading={loading} style={{ marginBottom: 16 }}>
        <div className="stat-strip">
          <div className="stat-cell">
            <Statistic title={t('admin.requestCount')} value={fmtInt(data?.summary.requests ?? 0)} />
          </div>
          <div className="stat-cell">
            <Statistic title={t('admin.errorRate')} value={data?.summary.errorRate ?? 0} suffix="%" precision={2} />
          </div>
          <div className="stat-cell">
            <Tooltip title={t('admin.tokensSummary', { in: fmtInt(data?.summary.promptTokens ?? 0), out: fmtInt(data?.summary.completionTokens ?? 0) })}>
              <Statistic title={t('admin.tokensInOut')} value={`${fmtTokens(data?.summary.promptTokens ?? 0)} / ${fmtTokens(data?.summary.completionTokens ?? 0)}`} />
            </Tooltip>
          </div>
          <div className="stat-cell">
            <Statistic title={t('admin.costEstimate')} value={data?.summary.cost ?? 0} formatter={(v) => <Money value={v as number} mode="cost" big />} />
            {data?.summary.unpriced ? <span className="stat-note">{t('admin.partiallyUnpriced')}</span> : null}
          </div>
        </div>
      </Card>
      <Card title={t('admin.byUser')} loading={loading} style={{ marginBottom: 16 }}>
        <Table<StatsGroup> rowKey="dim" size="small" scroll={{ x: 'max-content' }} pagination={{ pageSize: 20, hideOnSinglePage: true }} dataSource={data?.byUser ?? []} columns={groupColumns(t('common.user'))} />
      </Card>
      <Card title={t('admin.byModel')} loading={loading}>
        <Table<StatsGroup> rowKey="dim" size="small" scroll={{ x: 'max-content' }} pagination={false} dataSource={data?.byModel ?? []} columns={groupColumns(t('common.model'))} />
      </Card>
    </div>
  )
}

const SettingsTab: React.FC = () => {
  const { t } = useI18n()
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
        message.success(t('admin.rateSynced', { rate: res.rate, source: res.source }))
      } else {
        form.setFieldValue('exchangeRate', res.rate)
        message.success(t('admin.rateFetched', { rate: res.rate }))
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
          else message.success(t('admin.saved'))
        } catch (e) {
          message.error((e as Error).message)
        } finally {
          setSaving(false)
        }
      }}
    >
      <Form.Item name="registerMode" label={t('admin.registerMode')}>
        <Select
          options={[
            { value: 'open', label: t('admin.registerOpen') },
            { value: 'invite', label: t('admin.registerInvite') },
            { value: 'closed', label: t('admin.registerClosed') },
          ]}
        />
      </Form.Item>
      <Form.Item name="feishuEnabled" label={t('admin.feishuLogin')} valuePropName="checked">
        <Switch />
      </Form.Item>
      <Form.Item name="feishuAppId" label={t('admin.feishuAppId')}>
        <Input placeholder="cli_xxxx" />
      </Form.Item>
      <Form.Item name="feishuAppSecret" label={t('admin.feishuAppSecret') + (hasSecret ? t('admin.feishuSecretKeep') : '')}>
        <Input.Password placeholder={t('admin.secretPlaceholder')} />
      </Form.Item>
      <Form.Item name="feishuBaseUrl" label={t('admin.feishuBaseUrlLabel')}>
        <Input placeholder="https://open.feishu.cn" />
      </Form.Item>
      <Form.Item
        name="exchangeRateMode"
        label={t('admin.exchangeRateLabel')}
        extra={t('admin.exchangeRateExtra')}
      >
        <Radio.Group
          onChange={(e) => setFxMode(e.target.value)}
          options={[
            { value: 'auto', label: t('admin.fxAuto') },
            { value: 'manual', label: t('admin.fixedValue') },
          ]}
        />
      </Form.Item>
      {fxMode === 'auto' ? (
        <Form.Item label={t('admin.currentRate')}>
          <Space wrap>
            <span>
              1 USD = <Typography.Text strong>{fxInfo.rate ?? 7.2}</Typography.Text> CNY
            </span>
            {fxInfo.source && <Tag>{fxInfo.source}</Tag>}
            <Typography.Text type="secondary">{t('admin.rateUpdatedAt', { time: formatDateTime(fxInfo.updatedAt) })}</Typography.Text>
            <Button size="small" loading={syncing} onClick={() => doSync(true)}>
              {t('admin.syncNow')}
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
        <Form.Item label={t('admin.fixedValue')} required>
          <Space>
            <Form.Item name="exchangeRate" noStyle rules={[{ required: true, message: t('admin.exchangeRateRequired') }]}>
              <InputNumber min={0.5} max={20} step={0.1} style={{ width: 160 }} />
            </Form.Item>
            <Button size="small" loading={syncing} onClick={() => doSync(false)}>
              {t('admin.fetchLatest')}
            </Button>
          </Space>
        </Form.Item>
      )}
      <Button type="primary" htmlType="submit" loading={loading || saving}>
        {t('common.save')}
      </Button>
    </Form>
  )
}

const AdminPage: React.FC = () => {
  const { t } = useI18n()
  return (
    <div>
      <div className="page-heading">
        <div><h2>{t('admin.title')}</h2><p>{t('admin.subtitle')}</p></div>
      </div>
      <Tabs
        items={[
          { key: 'stats', label: t('admin.tabStats'), children: <StatsTab /> },
          { key: 'users', label: t('common.user'), children: <UsersTab /> },
          { key: 'models', label: t('admin.tabCatalog'), children: <CatalogModelsTab /> },
          { key: 'pricing', label: t('admin.tabPricing'), children: <PricingTab /> },
          { key: 'proxies', label: t('admin.tabProxies'), children: <ProxiesTab /> },
          { key: 'templates', label: t('admin.tabTemplates'), children: <TemplatesTab /> },
          { key: 'settings', label: t('admin.tabSettings'), children: <SettingsTab /> },
        ]}
      />
    </div>
  )
}

export default AdminPage
