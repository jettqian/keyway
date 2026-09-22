import React from 'react'
import { Button, Form, Input, Modal, Popconfirm, Space, Table, Tabs, Tag, message } from 'antd'
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

const CatalogTab: React.FC<{ channels: Channel[]; loading: boolean }> = ({ channels, loading }) => {
  const { t } = useI18n()
  const [catalog, setCatalog] = React.useState<CatalogModel[]>([])
  const [catLoading, setCatLoading] = React.useState(true)

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

  const addToAllChannels = async (model: CatalogModel) => {
    try {
      await updateModelBindings({ name: model.name, previousName: '', channelIds: channels.map((c) => c.id) })
      message.success(t('models.addedToAllChannels', { name: model.name }))
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
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
               <Button size="small" onClick={() => addToAllChannels(m)} disabled={channels.length === 0}>
                 {t('models.addToAllChannels')}
               </Button>
             ),
           },
        ]}
      />
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
          { key: 'catalog', label: t('models.catalogTab'), children: <CatalogTab channels={channels} loading={loading} /> },
        ]}
      />
    </div>
  )
}

export default ModelsPage
