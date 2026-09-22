import React from 'react'
import { Alert, Button, Checkbox, Form, Input, Modal, Popconfirm, Space, Table, Tabs, Tag, message } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { listChannels, updateModelBindings, listCatalogModels } from '../api'
import type { Channel, CatalogModel } from '../api/types'
import CatalogPrice from '../components/CatalogPrice'
import { useI18n } from '../i18n'

interface ModelRow {
  name: string
  channels: Channel[]
}

const MyModelsTab: React.FC<{ channels: Channel[]; loading: boolean; refresh: () => void }> = ({ channels, loading, refresh }) => {
  const { t } = useI18n()
  const [renaming, setRenaming] = React.useState<ModelRow | null>(null)
  const [form] = Form.useForm()

  const rows: ModelRow[] = React.useMemo(() => {
    const map = new Map<string, Channel[]>()
    for (const c of channels) {
      for (const m of Array.isArray(c.models) ? c.models : []) {
        const name = m.trim()
        if (!name) continue
        if (!map.has(name)) map.set(name, [])
        map.get(name)!.push(c)
      }
    }
    return [...map.entries()]
      .map(([name, chs]) => ({ name, channels: chs }))
      .sort((a, b) => a.name.localeCompare(b.name))
  }, [channels])

  const submitRename = async () => {
    if (!renaming) return
    const v = await form.validateFields()
    const name = (v.name as string).trim()
    if (name === renaming.name) {
      setRenaming(null)
      return
    }
    try {
      await updateModelBindings({ name, previousName: renaming.name, channelIds: renaming.channels.map((c) => c.id) })
      message.success(t('models.renamedNotice'))
      setRenaming(null)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const removeModel = async (row: ModelRow) => {
    try {
      await updateModelBindings({ name: row.name, previousName: row.name, channelIds: [] })
      message.success(t('models.removedFromAll'))
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <div className="tab-note">{t('models.mineTabNote')}</div>
      <Table<ModelRow>
        rowKey="name"
        loading={loading}
        dataSource={rows}
        scroll={{ x: 'max-content' }}
        locale={{ emptyText: t('models.mineEmpty') }}
        columns={[
          { title: t('common.model'), dataIndex: 'name', render: (n: string) => <Tag>{n}</Tag> },
          {
            title: t('models.channels'),
            render: (_: unknown, row: ModelRow) =>
              row.channels.length ? (
                <Space wrap>
                  {row.channels.map((c) => (
                    <Tag key={c.id} color={c.enabled ? 'blue' : undefined}>
                      {c.name}
                      {!c.enabled ? t('models.channelDisabledSuffix') : ''}
                    </Tag>
                  ))}
                </Space>
              ) : (
                <span className="text-tertiary">—</span>
              ),
          },
          {
            title: t('common.action'),
            width: 160,
            render: (_: unknown, row: ModelRow) => (
              <Space>
                <Button size="small" onClick={() => { setRenaming(row); form.setFieldsValue({ name: row.name }) }}>{t('models.rename')}</Button>
                <Popconfirm title={t('models.deleteConfirm', { name: row.name })} onConfirm={() => removeModel(row)}>
                  <Button size="small" danger>{t('common.delete')}</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal
        title={t('models.renameTitle', { name: renaming?.name ?? '' })}
        open={renaming !== null}
        onOk={submitRename}
        onCancel={() => setRenaming(null)}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t('models.newName')} rules={[{ required: true, message: t('models.newNameRequired') }]} extra={<span className="form-hint">{t('models.renameHint')}</span>}>
            <Input placeholder={t('models.namePlaceholder')} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

const CatalogTab: React.FC<{ channels: Channel[]; loading: boolean; refresh: () => void }> = ({ channels, loading, refresh }) => {
  const { t } = useI18n()
  const [catalog, setCatalog] = React.useState<CatalogModel[]>([])
  const [catLoading, setCatLoading] = React.useState(true)
  const [bindingModel, setBindingModel] = React.useState<CatalogModel | null>(null)
  const [bindingChannelIDs, setBindingChannelIDs] = React.useState<number[]>([])
  const [bindingSaving, setBindingSaving] = React.useState(false)

  React.useEffect(() => {
    listCatalogModels()
      .then((r) => setCatalog(r.models))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setCatLoading(false))
  }, [])

  const usage = React.useMemo(() => {
    const map = new Map<string, string[]>()
    for (const c of channels) {
      for (const m of Array.isArray(c.models) ? c.models : []) {
        const name = m.trim()
        if (!name) continue
        if (!map.has(name)) map.set(name, [])
        map.get(name)!.push(c.name)
      }
    }
    return map
  }, [channels])

  const openBinding = (model: CatalogModel) => {
    const used = new Set(usage.get(model.name) ?? [])
    setBindingModel(model)
    setBindingChannelIDs(channels.filter((c) => used.has(c.name)).map((c) => c.id))
  }

  const saveBinding = async () => {
    if (!bindingModel) return
    setBindingSaving(true)
    try {
      await updateModelBindings({ name: bindingModel.name, previousName: '', channelIds: bindingChannelIDs })
      message.success(t('models.bindingSaved'))
      setBindingModel(null)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setBindingSaving(false)
    }
  }

  const pendingModels = catalog.filter((model) => !usage.has(model.name))

  return (
    <div>
      {pendingModels.length > 0 ? (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message={t('models.newModelsNotice')}
          description={t('models.newModelsNoticeDesc', { models: pendingModels.map((model) => model.name).join('、') })}
        />
      ) : null}
      <div className="tab-note">{t('models.catalogTabNote')}</div>
      <Table<CatalogModel>
        rowKey="id"
        loading={loading || catLoading}
        dataSource={catalog}
        scroll={{ x: 'max-content' }}
        locale={{ emptyText: t('models.catalogEmpty') }}
        columns={[
          { title: t('common.model'), dataIndex: 'name', render: (n: string) => <Tag>{n}</Tag> },
          {
            title: t('models.unitPrice'),
            width: 230,
            render: (_: unknown, m: CatalogModel) => <CatalogPrice m={m} />,
          },
          { title: t('common.note'), dataIndex: 'note', ellipsis: true, render: (v: string) => v || '-' },
           {
             title: t('models.usage'),
            width: 220,
            render: (_: unknown, m: CatalogModel) => {
              const chans = usage.get(m.name)
              if (!chans || chans.length === 0) return <span className="text-tertiary">{t('models.unused')}</span>
              return <span>{t('models.usedByChannels', { count: chans.length })}</span>
             },
           },
           {
             title: t('common.action'),
             width: 190,
             render: (_: unknown, m: CatalogModel) => (
             <Button size="small" onClick={() => openBinding(m)} disabled={channels.length === 0}>
                 {t('models.selectChannels')}
             </Button>
             ),
           },
        ]}
      />
      <Modal
        title={bindingModel ? t('models.selectChannelsTitle', { name: bindingModel.name }) : ''}
        open={bindingModel !== null}
        onOk={saveBinding}
        okButtonProps={{ loading: bindingSaving }}
        onCancel={() => setBindingModel(null)}
        destroyOnClose
      >
        <Checkbox.Group
          value={bindingChannelIDs}
          onChange={(ids) => setBindingChannelIDs(ids as number[])}
          style={{ display: 'flex', flexDirection: 'column', gap: 10 }}
          options={channels.map((c) => ({ label: `${c.name}${c.enabled ? '' : t('models.channelDisabledSuffix')}`, value: c.id }))}
        />
      </Modal>
    </div>
  )
}

const ModelsPage: React.FC = () => {
  const { t } = useI18n()
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [loading, setLoading] = React.useState(true)

  const refresh = React.useCallback(() => {
    setLoading(true)
    listChannels()
      .then((r) => setChannels(r.channels))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  React.useEffect(refresh, [refresh])

  return (
    <div>
      <div className="page-heading">
        <div><h2>{t('models.title')}</h2><p>{t('models.subtitle')}</p></div>
        <div className="page-actions"><Button icon={<ReloadOutlined />} onClick={refresh}>{t('models.refresh')}</Button></div>
      </div>
      <Tabs
        items={[
          { key: 'mine', label: t('models.myModels'), children: <MyModelsTab channels={channels} loading={loading} refresh={refresh} /> },
          { key: 'catalog', label: t('models.catalogTab'), children: <CatalogTab channels={channels} loading={loading} refresh={refresh} /> },
        ]}
      />
    </div>
  )
}

export default ModelsPage
