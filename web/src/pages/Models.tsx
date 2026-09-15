import React from 'react'
import { Button, Checkbox, Form, Input, Modal, Popconfirm, Space, Table, Tag, message } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { listChannels, updateModelBindings } from '../api'
import type { Channel } from '../api/types'

interface ModelRow {
  name: string
  channels: Channel[]
}

const ModelsPage: React.FC = () => {
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [loading, setLoading] = React.useState(true)
  const [mode, setMode] = React.useState<'create' | 'edit' | 'rename'>('create')
  const [current, setCurrent] = React.useState<ModelRow | null>(null)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [form] = Form.useForm()

  const refresh = React.useCallback(() => {
    setLoading(true)
    listChannels()
      .then((r) => setChannels(r.channels))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  React.useEffect(refresh, [refresh])

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

  const openCreate = () => {
    setMode('create')
    setCurrent(null)
    form.resetFields()
    form.setFieldsValue({ name: '', channelIds: [] })
    setModalOpen(true)
  }

  const openEdit = (row: ModelRow) => {
    setMode('edit')
    setCurrent(row)
    form.setFieldsValue({ channelIds: row.channels.map((c) => c.id) })
    setModalOpen(true)
  }

  const openRename = (row: ModelRow) => {
    setMode('rename')
    setCurrent(row)
    form.setFieldsValue({ name: row.name })
    setModalOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    try {
      if (mode === 'create') {
        await updateModelBindings({ name: v.name.trim(), previousName: '', channelIds: v.channelIds || [] })
        message.success('模型已创建')
      } else if (mode === 'edit' && current) {
        await updateModelBindings({ name: current.name, previousName: current.name, channelIds: v.channelIds || [] })
        message.success('绑定已保存，下一个请求生效')
      } else if (mode === 'rename' && current) {
        const name = (v.name as string).trim()
        if (name === current.name) {
          setModalOpen(false)
          return
        }
        await updateModelBindings({ name, previousName: current.name, channelIds: current.channels.map((c) => c.id) })
        message.success('模型已重命名')
      }
      setModalOpen(false)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const removeModel = async (row: ModelRow) => {
    try {
      await updateModelBindings({ name: row.name, previousName: row.name, channelIds: [] })
      message.success('模型已删除')
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const channelOptions = channels.map((c) => ({
    label: (
      <Space>
        <span>{c.name}</span>
        {c.enabled ? null : <Tag>停用</Tag>}
        <span style={{ color: '#999' }}>优先级 {c.priority}</span>
      </Space>
    ),
    value: c.id,
  }))

  const modalTitle =
    mode === 'create' ? '新建模型' : mode === 'rename' ? `重命名模型：${current?.name}` : `绑定渠道：${current?.name}`

  return (
    <div>
      <div className="page-heading">
        <div><h2>模型管理</h2><p>维护模型目录及其与渠道的绑定关系；渠道本身的配置在渠道页维护。</p></div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建模型</Button></div>
      </div>
      <Table<ModelRow>
        rowKey="name"
        loading={loading}
        dataSource={rows}
        locale={{ emptyText: '暂无模型：直接新建，或在渠道页为渠道填写模型列表' }}
        columns={[
          { title: '模型', dataIndex: 'name', render: (n: string) => <Tag>{n}</Tag> },
          {
            title: '绑定渠道',
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
                <span style={{ color: '#999' }}>未绑定渠道</span>
              ),
          },
          {
            title: '操作',
            width: 220,
            render: (_: unknown, row: ModelRow) => (
              <Space>
                <a onClick={() => openEdit(row)}>绑定渠道</a>
                <a onClick={() => openRename(row)}>重命名</a>
                <Popconfirm title={`删除模型 ${row.name}？将从所有渠道移除`} onConfirm={() => removeModel(row)}>
                  <a style={{ color: 'red' }}>删除</a>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={modalTitle} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          {mode === 'create' ? (
            <>
              <Form.Item name="name" label="模型名" rules={[{ required: true, message: '请输入模型名' }]}>
                <Input placeholder="如 claude-sonnet-4.5" />
              </Form.Item>
              <Form.Item name="channelIds" label="绑定渠道" extra={<span className="form-hint">点选该模型可路由到的渠道；同一模型可绑定多个渠道，按渠道优先级路由。</span>}>
                <Checkbox.Group options={channelOptions} style={{ display: 'flex', flexDirection: 'column', gap: 4 }} />
              </Form.Item>
            </>
          ) : mode === 'rename' ? (
            <Form.Item name="name" label="新模型名" rules={[{ required: true, message: '请输入新模型名' }]} extra={<span className="form-hint">重命名会同步更新所有绑定渠道及模型映射。</span>}>
              <Input placeholder="如 claude-sonnet-4.5" />
            </Form.Item>
          ) : (
            <Form.Item name="channelIds" label="绑定渠道" extra={<span className="form-hint">点选该模型可路由到的渠道；不选则该模型对所有渠道不生效。</span>}>
              <Checkbox.Group options={channelOptions} style={{ display: 'flex', flexDirection: 'column', gap: 4 }} />
            </Form.Item>
          )}
        </Form>
      </Modal>
    </div>
  )
}

export default ModelsPage
