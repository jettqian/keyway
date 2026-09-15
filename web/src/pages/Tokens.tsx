import React from 'react'
import { Table, Button, Modal, Form, Input, Select, DatePicker, Tag, message, Popconfirm, Typography, Space } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { listTokens, createToken, revokeToken, revealToken, listChannels } from '../api'
import type { GatewayToken, Channel } from '../api/types'
import { formatDateTime } from '../format'

const TokensPage: React.FC = () => {
  const [tokens, setTokens] = React.useState<GatewayToken[]>([])
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [created, setCreated] = React.useState<string | null>(null)
  const [revealed, setRevealed] = React.useState<string | null>(null)
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

  return (
    <div>
      <div className="page-heading">
        <div><h2>网关令牌</h2><p>为客户端创建访问凭证，可限制渠道与模型范围。</p></div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={() => { form.resetFields(); form.setFieldsValue({ name: '', channelIds: [], modelScope: '', expiresAt: undefined }); setModalOpen(true) }}>
          新建令牌
        </Button>
      </div>
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
            dataIndex: 'channelIds',
            render: (_: number[] | undefined, t: GatewayToken) => {
              const ids = t.channelIds ?? (t.channelId ? [t.channelId] : [])
              if (ids.length === 0) return '不限'
              return ids
                .map((id) => channels.find((c) => c.id === id)?.name ?? `#${id}`)
                .join('、')
            },
          },
          { title: '模型范围', dataIndex: 'modelScope', render: (v?: string) => v ?? '不限' },
          { title: '创建时间', dataIndex: 'createdAt', render: (v: number | string) => formatDateTime(v) },
          { title: '过期时间', dataIndex: 'expiresAt', render: (v?: number | string) => v ? formatDateTime(v) : '永不过期' },
          {
            title: '状态',
            dataIndex: 'revoked',
            render: (r: boolean) => (r ? <Tag>已吊销</Tag> : <Tag color="green">有效</Tag>),
          },
          {
            title: '操作',
            width: 160,
            render: (_, t) =>
              <Space>
                <Popconfirm title="显示完整令牌密钥？" onConfirm={async () => {
                    try {
                      const r = await revealToken(t.id)
                      setRevealed(r.plaintext)
                    } catch (e) {
                      message.error((e as Error).message)
                    }
                  }}>
                  <a>查看密钥</a>
                </Popconfirm>
                {t.revoked ? null : (
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
                )}
              </Space>,
          },
        ]}
      />
      <Modal title="新建令牌" open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input placeholder="如 claude-code" />
          </Form.Item>
          <Form.Item name="channelIds" label="限定渠道" extra={<span className="form-hint">留空则允许访问所有启用渠道。</span>}>
            <Select
              mode="multiple"
              allowClear
              options={channels.map((c) => ({ value: c.id, label: c.name }))}
              placeholder="不限定则路由到所有启用渠道"
            />
          </Form.Item>
          <Form.Item name="modelScope" label="模型范围（可选）" extra={<span className="form-hint">支持前缀通配，例如 claude-*。</span>}>
            <Input placeholder="如 claude-*" />
          </Form.Item>
          <Form.Item name="expiresAt" label="过期时间（可选）">
            <DatePicker showTime style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>
      <Modal
        open={revealed !== null}
        title="令牌密钥"
        onCancel={() => setRevealed(null)}
        onOk={() => setRevealed(null)}
      >
        <Typography.Paragraph>仅本人可查看，复制后请妥善保管。</Typography.Paragraph>
        <Typography.Paragraph copyable={{ text: revealed ?? '' }} code>
          {revealed}
        </Typography.Paragraph>
      </Modal>
      <Modal
        open={created !== null}
        title="令牌已创建"
        onCancel={() => setCreated(null)}
        onOk={() => setCreated(null)}
      >
        <Typography.Paragraph>请复制保存；之后也可在令牌列表中回看：</Typography.Paragraph>
        <Typography.Paragraph copyable={{ text: created ?? '' }} code>
          {created}
        </Typography.Paragraph>
      </Modal>
    </div>
  )
}

export default TokensPage
