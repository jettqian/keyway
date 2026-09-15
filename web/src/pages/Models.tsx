import React from 'react'
import { Button, Checkbox, Form, Input, Modal, Space, Table, Tag, message } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { listChannels, updateModelBindings } from '../api'
import type { Channel } from '../api/types'

interface ModelRow {
  name: string
  bindings: Channel[]
}

const ModelsPage: React.FC = () => {
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<ModelRow | null>(null)
  const [form] = Form.useForm()

  const refresh = React.useCallback(() => {
    setLoading(true)
    listChannels()
      .then((r) => setChannels(r.channels))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  React.useEffect(refresh, [refresh])

  const rows = React.useMemo<ModelRow[]>(() => {
    const map = new Map<string, Channel[]>()
    for (const channel of channels) {
      for (const model of Array.isArray(channel.models) ? channel.models : []) {
        if (!model) continue
        const list = map.get(model) ?? []
        list.push(channel)
        map.set(model, list)
      }
    }
    return [...map.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([name, bindings]) => ({ name, bindings }))
  }, [channels])

  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ name: '', channelIds: channels.filter((c) => c.enabled).map((c) => c.id) })
    setModalOpen(true)
  }

  const openEdit = (row: ModelRow) => {
    setEditing(row)
    form.setFieldsValue({ name: row.name, channelIds: row.bindings.map((c) => c.id) })
    setModalOpen(true)
  }

  const submit = async () => {
    const values = await form.validateFields()
    const name = String(values.name).trim()
    try {
      await updateModelBindings({ name, previousName: editing?.name ?? '', channelIds: values.channelIds ?? [] })
      message.success('模型绑定已保存，下一个请求生效')
      setModalOpen(false)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <div className="page-heading">
        <div><h2>模型管理</h2><p>一个渠道可以绑定多个模型；此页用于批量加入或移出渠道。</p></div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新增模型绑定</Button></div>
      </div>
      <Table<ModelRow>
        rowKey="name"
        loading={loading}
        dataSource={rows}
        columns={[
          { title: '模型名', dataIndex: 'name' },
          {
            title: '绑定渠道',
            render: (_: unknown, row: ModelRow) => (
              <Space wrap>{row.bindings.map((c) => <Tag key={c.id} color={c.enabled ? 'blue' : undefined}>{c.name} · 优先级 {c.priority}</Tag>)}</Space>
            ),
          },
          { title: '可用渠道数', render: (_: unknown, row: ModelRow) => row.bindings.filter((c) => c.enabled).length },
          { title: '操作', render: (_: unknown, row: ModelRow) => <a onClick={() => openEdit(row)}>编辑绑定</a> },
        ]}
      />
      <Modal title={editing ? `编辑模型：${editing.name}` : '新增模型绑定'} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="模型名" rules={[{ required: true, message: '请输入模型名' }]}>
            <Input placeholder="如 claude-sonnet-4" />
          </Form.Item>
          <Form.Item name="channelIds" label="绑定渠道" extra="渠道停用后仍保留绑定，重新启用即可恢复路由。">
            <Checkbox.Group style={{ display: 'grid', gap: 8 }}>
              {channels.map((c) => <Checkbox key={c.id} value={c.id}>{c.name}（{c.type}，优先级 {c.priority}）</Checkbox>)}
            </Checkbox.Group>
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

export default ModelsPage
