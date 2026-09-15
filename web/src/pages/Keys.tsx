import React from 'react'
import { Table, Button, Modal, Form, Input, Tag, message, Popconfirm, Space } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { listKeys, createKey, updateKey, updateKeyStatus, deleteKey } from '../api'
import type { ApiKey } from '../api/types'
import { formatDateTime } from '../format'
import { useI18n } from '../i18n'

const KeysPage: React.FC = () => {
  const { t } = useI18n()
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
        message.success(t('common.updated'))
      } else {
        await createKey(values)
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
      <div className="page-heading">
        <div><h2>{t('keys.title')}</h2><p>{t('keys.subtitle')}</p></div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>{t('keys.create')}</Button></div>
      </div>
      <Table<ApiKey>
        rowKey="id"
        loading={loading}
        dataSource={keys}
        scroll={{ x: 'max-content' }}
        locale={{ emptyText: t('keys.empty') }}
        columns={[
          { title: t('common.name'), dataIndex: 'name' },
          { title: t('common.note'), dataIndex: 'note', ellipsis: true },
          {
            title: t('common.status'),
            dataIndex: 'status',
            width: 120,
            render: (s: number, k: ApiKey) =>
              k.cooldownUntil > Date.now() / 1000 ? (
                <Tag color="orange">{t('keys.cooldown')}</Tag>
              ) : s === 1 ? (
                <Tag color="green">{t('common.enabled')}</Tag>
              ) : (
                <Tag>{t('keys.statusDisabled')}</Tag>
              ),
          },
          {
            title: t('keys.lastError'),
            dataIndex: 'lastError',
            ellipsis: true,
            render: (v: string) => v || '-',
          },
          { title: t('common.createdAt'), dataIndex: 'createdAt', width: 180, render: (v: number | string) => formatDateTime(v) },
          {
            title: t('common.action'),
            width: 200,
            render: (_, k) => (
              <Space>
                <Button size="small" onClick={() => openEdit(k)}>{t('common.edit')}</Button>
                <Button
                  size="small"
                  onClick={async () => {
                    try {
                      await updateKeyStatus(k.id, k.status === 1 ? 2 : 1)
                      message.success(k.status === 1 ? t('keys.disableSuccess') : t('keys.enableSuccess'))
                      refresh()
                    } catch (e) {
                      message.error((e as Error).message)
                    }
                  }}
                >
                  {k.status === 1 ? t('common.disabled') : t('common.enabled')}
                </Button>
                <Popconfirm
                  title={t('keys.confirmDelete')}
                  onConfirm={async () => {
                    await deleteKey(k.id)
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
      <Modal
        title={editing ? t('keys.edit') : t('keys.create')}
        open={modalOpen}
        onOk={submit}
        onCancel={() => setModalOpen(false)}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('common.nameRequired') }]}>
            <Input placeholder={t('keys.namePlaceholder')} />
          </Form.Item>
          <Form.Item name="value" label={editing ? t('keys.valueKeep') : t('keys.value')} extra={<span className="form-hint">{t('keys.valueHint')}</span>}>
            <Input.Password placeholder="sk-..." />
          </Form.Item>
          <Form.Item name="note" label={t('common.note')}>
            <Input.TextArea rows={2} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

export default KeysPage
