import React from 'react'
import { Table, Button, Modal, Form, Input, Tag, message, Popconfirm, Space } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { listKeys, createKey, updateKey, updateKeyStatus, deleteKey } from '../api'
import type { ApiKey } from '../api/types'
import { formatDateTime } from '../format'

const KeysPage: React.FC = () => {
  const [keys, setKeys] = React.useState<ApiKey[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<ApiKey | null>(null)
  const [form] = Form.useForm()

  const refresh = React.useCallback(() => {
    setLoading(true)
    listKeys()
      .then((r) => setKeys(r.keys))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  React.useEffect(refresh, [refresh])

  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ name: '', value: '', note: '' })
    setModalOpen(true)
  }

  const openEdit = (k: ApiKey) => {
    setEditing(k)
    form.setFieldsValue({ name: k.name, note: k.note, value: '' })
    setModalOpen(true)
  }

  const submit = async () => {
    const values = await form.validateFields()
    try {
      if (editing) {
        await updateKey(editing.id, values)
        message.success('已更新')
      } else {
        await createKey(values)
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
      <div className="page-heading">
        <div><h2>上游密钥池</h2><p>集中保存并复用上游 API 密钥，密钥值只在这里管理。</p></div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建密钥</Button></div>
      </div>
      <Table<ApiKey>
        rowKey="id"
        loading={loading}
        dataSource={keys}
        locale={{ emptyText: '暂无密钥，点击右上角「新建密钥」添加' }}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '备注', dataIndex: 'note', ellipsis: true },
          {
            title: '状态',
            dataIndex: 'status',
            width: 120,
            render: (s: number, k: ApiKey) =>
              k.cooldownUntil > Date.now() / 1000 ? (
                <Tag color="orange">冷却中</Tag>
              ) : s === 1 ? (
                <Tag color="green">启用</Tag>
              ) : (
                <Tag>已停用</Tag>
              ),
          },
          {
            title: '最近错误',
            dataIndex: 'lastError',
            ellipsis: true,
            render: (v: string) => v || '-',
          },
          { title: '创建时间', dataIndex: 'createdAt', width: 180, render: (v: number | string) => formatDateTime(v) },
          {
            title: '操作',
            width: 200,
            render: (_, k) => (
              <Space>
                <a onClick={() => openEdit(k)}>编辑</a>
                <a
                  onClick={async () => {
                    try {
                      await updateKeyStatus(k.id, k.status === 1 ? 2 : 1)
                      message.success(k.status === 1 ? '已停用，不再参与渠道轮换' : '已启用')
                      refresh()
                    } catch (e) {
                      message.error((e as Error).message)
                    }
                  }}
                >
                  {k.status === 1 ? '停用' : '启用'}
                </a>
                <Popconfirm
                  title="删除该密钥？绑定它的渠道将失效"
                  onConfirm={async () => {
                    await deleteKey(k.id)
                    message.success('已删除')
                    refresh()
                  }}
                >
                  <a className="danger-link">删除</a>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal
        title={editing ? '编辑密钥' : '新建密钥'}
        open={modalOpen}
        onOk={submit}
        onCancel={() => setModalOpen(false)}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input placeholder="如 openai-主号" />
          </Form.Item>
          <Form.Item name="value" label={editing ? '密钥值（留空则不修改）' : '密钥值'} extra={<span className="form-hint">仅用于连接上游服务，保存后不会再次完整展示。</span>}>
            <Input.Password placeholder="sk-..." />
          </Form.Item>
          <Form.Item name="note" label="备注">
            <Input.TextArea rows={2} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

export default KeysPage
