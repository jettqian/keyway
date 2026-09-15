import React from 'react'
import { Button, Form, Input, Modal, Popconfirm, Space, Table, Tabs, Tag, Tooltip, message } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { listChannels, updateModelBindings, listCatalogModels } from '../api'
import type { Channel, CatalogModel } from '../api/types'

interface ModelRow {
  name: string
  channels: Channel[]
}

const MyModelsTab: React.FC<{ channels: Channel[]; loading: boolean; refresh: () => void }> = ({ channels, loading, refresh }) => {
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
      message.success('已重命名，下一个请求生效')
      setRenaming(null)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const removeModel = async (row: ModelRow) => {
    try {
      await updateModelBindings({ name: row.name, previousName: row.name, channelIds: [] })
      message.success('模型已从所有渠道移除')
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <div style={{ color: '#777', fontSize: 13, marginBottom: 12 }}>
        模型与渠道的关联在渠道表单中点选维护：编辑渠道时可从模型目录或已有模型点选，也可直接输入新名称。此处仅管理模型名本身。
      </div>
      <Table<ModelRow>
        rowKey="name"
        loading={loading}
        dataSource={rows}
        locale={{ emptyText: '暂无模型：在渠道表单中点选或输入模型名后即出现在这里' }}
        columns={[
          { title: '模型', dataIndex: 'name', render: (n: string) => <Tag>{n}</Tag> },
          {
            title: '服务渠道',
            render: (_: unknown, row: ModelRow) =>
              row.channels.length ? (
                <Space wrap>
                  {row.channels.map((c) => (
                    <Tag key={c.id} color={c.enabled ? 'blue' : undefined}>
                      {c.name}
                      {!c.enabled ? '（停用）' : ''}
                    </Tag>
                  ))}
                </Space>
              ) : (
                <span style={{ color: '#999' }}>—</span>
              ),
          },
          {
            title: '操作',
            width: 160,
            render: (_: unknown, row: ModelRow) => (
              <Space>
                <a onClick={() => { setRenaming(row); form.setFieldsValue({ name: row.name }) }}>重命名</a>
                <Popconfirm title={`删除模型 ${row.name}？将从所有渠道移除`} onConfirm={() => removeModel(row)}>
                  <a style={{ color: 'red' }}>删除</a>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal
        title={`重命名模型：${renaming?.name ?? ''}`}
        open={renaming !== null}
        onOk={submitRename}
        onCancel={() => setRenaming(null)}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="新模型名" rules={[{ required: true, message: '请输入新模型名' }]} extra={<span className="form-hint">重命名会同步更新所有绑定渠道及模型映射。</span>}>
            <Input placeholder="如 claude-sonnet-4.5" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

const CatalogTab: React.FC<{ channels: Channel[]; loading: boolean }> = ({ channels, loading }) => {
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

  return (
    <div>
      <div style={{ color: '#777', fontSize: 13, marginBottom: 12 }}>
        管理员预置的模型目录，作为渠道表单中的点选数据源；未列出的模型仍可在渠道表单中手动输入。
        单价按模型名关联价目表（费用统计同口径）。
      </div>
      <Table<CatalogModel>
        rowKey="id"
        loading={loading || catLoading}
        dataSource={catalog}
        locale={{ emptyText: '管理员尚未配置模型目录' }}
        columns={[
          { title: '模型', dataIndex: 'name', render: (n: string) => <Tag>{n}</Tag> },
          {
            title: '单价（$/百万 tokens）',
            width: 190,
            render: (_: unknown, m: CatalogModel) =>
              m.inputPerM != null ? (
                <span>
                  输入 {m.inputPerM} / 输出 {m.outputPerM}
                </span>
              ) : (
                <Tooltip title="价目表中无同名条目，使用该模型的请求费用将记为未定价">
                  <span style={{ color: '#999' }}>未定价</span>
                </Tooltip>
              ),
          },
          { title: '备注', dataIndex: 'note', ellipsis: true, render: (v: string) => v || '-' },
          {
            title: '使用情况',
            width: 220,
            render: (_: unknown, m: CatalogModel) => {
              const chans = usage.get(m.name)
              if (!chans || chans.length === 0) return <span style={{ color: '#999' }}>未使用</span>
              return <span>已用于 {chans.length} 个渠道</span>
            },
          },
        ]}
      />
    </div>
  )
}

const ModelsPage: React.FC = () => {
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
        <div><h2>模型管理</h2><p>管理模型目录；渠道关联在渠道表单中点选维护。</p></div>
        <div className="page-actions"><Button icon={<ReloadOutlined />} onClick={refresh}>刷新</Button></div>
      </div>
      <Tabs
        items={[
          { key: 'mine', label: '我的模型', children: <MyModelsTab channels={channels} loading={loading} refresh={refresh} /> },
          { key: 'catalog', label: '模型目录（管理员预置）', children: <CatalogTab channels={channels} loading={loading} /> },
        ]}
      />
    </div>
  )
}

export default ModelsPage
