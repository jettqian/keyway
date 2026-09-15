import React from 'react'
import { Button, Form, Modal, Select, Space, Table, Tag, message } from 'antd'
import { EditOutlined } from '@ant-design/icons'
import { listChannels, updateChannel } from '../api'
import type { Channel, ChannelInput } from '../api/types'

const toInput = (c: Channel, models: string[]): ChannelInput => ({
  name: c.name,
  type: c.type,
  baseUrls: c.baseUrls,
  keyIds: c.keyIds,
  keyStrategy: c.keyStrategy,
  lineStrategy: c.lineStrategy,
  allowPublicProxy: c.allowPublicProxy,
  models,
  modelMapping: c.modelMapping,
  priority: c.priority,
  priceMultiplier: c.priceMultiplier ?? 1,
  pricingMode: c.pricingMode ?? 'usd',
  cnyRatio: c.cnyRatio ?? 0,
  isDefault: c.isDefault,
  enabled: c.enabled,
  forwardMode: c.forwardMode ?? 'passthrough',
})

const ModelsPage: React.FC = () => {
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [loading, setLoading] = React.useState(true)
  const [editing, setEditing] = React.useState<Channel | null>(null)
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

  const openEdit = (channel: Channel) => {
    setEditing(channel)
    form.setFieldsValue({ models: Array.isArray(channel.models) ? channel.models : [] })
    setModalOpen(true)
  }

  const submit = async () => {
    if (!editing) return
    const values = await form.validateFields()
    const models = [...new Set(((values.models ?? []) as string[]).map((m) => m.trim()).filter(Boolean))]
    try {
      await updateChannel(editing.id, toInput(editing, models))
      message.success('渠道模型已保存，下一个请求生效')
      setModalOpen(false)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <div className="page-heading">
        <div><h2>渠道模型</h2><p>以渠道为主维护模型列表。一个渠道可以选择多个模型，同一模型也可以出现在多个渠道。</p></div>
      </div>
      <Table<Channel>
        rowKey="id"
        loading={loading}
        dataSource={channels}
        columns={[
          {
            title: '渠道',
            render: (_: unknown, c: Channel) => <Space><span>{c.name}</span><Tag color={c.enabled ? 'green' : undefined}>{c.enabled ? '启用' : '停用'}</Tag></Space>,
          },
          { title: '协议', dataIndex: 'type', render: (type: string) => <Tag>{type}</Tag> },
          { title: '优先级', dataIndex: 'priority' },
          {
            title: '已选模型',
            render: (_: unknown, c: Channel) => {
              const models = Array.isArray(c.models) ? c.models : []
              return models.length ? <Space wrap>{models.map((m) => <Tag key={m}>{m}</Tag>)}</Space> : <span style={{ color: '#999' }}>未选择模型</span>
            },
          },
          { title: '操作', width: 130, render: (_: unknown, c: Channel) => <Button type="link" icon={<EditOutlined />} onClick={() => openEdit(c)}>选择模型</Button> },
        ]}
      />
      <Modal title={editing ? `选择模型：${editing.name}` : '选择模型'} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="models" label="该渠道提供的模型" extra="模型列表属于渠道基础配置；回车即可添加多个模型。">
            <Select mode="tags" tokenSeparators={[',']} placeholder="输入模型名并回车" open={false} />
          </Form.Item>
          <div style={{ color: '#777', fontSize: 13 }}>渠道优先级：{editing?.priority ?? 0}。同一模型出现在多个渠道时，网关按渠道优先级选择。</div>
        </Form>
      </Modal>
    </div>
  )
}

export default ModelsPage
