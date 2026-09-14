import React from 'react'
import { Table, Button, Modal, Form, Input, Select, DatePicker, Tag, message, Popconfirm, Typography } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { listTokens, createToken, revokeToken, listChannels } from '../api'
import type { GatewayToken, Channel } from '../api/types'

const TokensPage: React.FC = () => {
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

  const submit = async () => {
    const v = await form.validateFields()
    try {
      const r = await createToken({
        name: v.name,
        channelId: v.channelId,
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

  return (
    <div>
      <div style={{ marginBottom: 16, display: 'flex', justifyContent: 'space-between' }}>
        <h2 style={{ margin: 0 }}>网关令牌</h2>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => { form.resetFields(); setModalOpen(true) }}>
          新建令牌
        </Button>
      </div>
      <Table<GatewayToken>
        rowKey="id"
        loading={loading}
        dataSource={tokens}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '前缀', dataIndex: 'keyPrefix' },
          {
            title: '限定渠道',
            dataIndex: 'channelId',
            render: (id?: number) => channels.find((c) => c.id === id)?.name ?? '不限',
          },
          { title: '模型范围', dataIndex: 'modelScope', render: (v?: string) => v ?? '不限' },
          { title: '过期时间', dataIndex: 'expiresAt', render: (v?: string) => v ?? '永不过期' },
          {
            title: '状态',
            dataIndex: 'revoked',
            render: (r: boolean) => (r ? <Tag>已吊销</Tag> : <Tag color="green">有效</Tag>),
          },
          {
            title: '操作',
            width: 100,
            render: (_, t) =>
              t.revoked ? null : (
                <Popconfirm
                  title="吊销后立即生效？"
                  onConfirm={async () => {
                    await revokeToken(t.id)
                    message.success('已吊销')
                    refresh()
                  }}
                >
                  <a style={{ color: 'red' }}>吊销</a>
                </Popconfirm>
              ),
          },
        ]}
      />
      <Modal title="新建令牌" open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input placeholder="如 claude-code" />
          </Form.Item>
          <Form.Item name="channelId" label="限定渠道（可选）">
            <Select
              allowClear
              options={channels.map((c) => ({ value: c.id, label: c.name }))}
              placeholder="不限定"
            />
          </Form.Item>
          <Form.Item name="modelScope" label="模型范围（前缀通配，可选）">
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
        onCancel={() => setCreated(null)}
        onOk={() => setCreated(null)}
      >
        <Typography.Paragraph>请立即复制保存，令牌明文仅展示一次：</Typography.Paragraph>
        <Typography.Paragraph copyable={{ text: created ?? '' }} code>
          {created}
        </Typography.Paragraph>
      </Modal>
    </div>
  )
}

export default TokensPage
