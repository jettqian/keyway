import React from 'react'
import { Table, Button, Modal, Form, Input, Tag, message, Popconfirm, Space, Steps, Alert, Typography } from 'antd'
import { PlusOutlined, SyncOutlined } from '@ant-design/icons'
import { listKeys, createKey, updateKey, updateKeyStatus, deleteKey, refreshKey, startChatGPTLogin, completeChatGPTLogin } from '../api'
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

  // ChatGPT 登录向导状态（步骤 0 授权 / 1 粘贴回调）
  const [wizardOpen, setWizardOpen] = React.useState(false)
  const [wizardStep, setWizardStep] = React.useState(0)
  const [authUrl, setAuthUrl] = React.useState('')
  const [callbackUrl, setCallbackUrl] = React.useState('')
  const [accountName, setAccountName] = React.useState('')
  const [wizardBusy, setWizardBusy] = React.useState(false)

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

  const openWizard = async () => {
    setWizardBusy(true)
    try {
      const r = await startChatGPTLogin()
      setAuthUrl(r.authUrl)
      setCallbackUrl('')
      setAccountName('')
      setWizardStep(0)
      setWizardOpen(true)
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setWizardBusy(false)
    }
  }

  const wizardContinue = () => {
    window.open(authUrl, '_blank', 'noopener')
    setWizardStep(1)
  }

  const wizardComplete = async () => {
    if (!callbackUrl.trim()) {
      message.warning(t('keys.chatgptCallback'))
      return
    }
    setWizardBusy(true)
    try {
      await completeChatGPTLogin(callbackUrl.trim(), accountName.trim() || undefined)
      message.success(t('keys.chatgptDone'))
      setWizardOpen(false)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setWizardBusy(false)
    }
  }

  const doRefreshKey = async (k: ApiKey) => {
    try {
      await refreshKey(k.id)
      message.success(t('keys.refreshSuccess'))
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const isChatgpt = (k: ApiKey) => k.kind === 'chatgpt'
  const hasChatgpt = keys.some(isChatgpt)

  return (
    <div>
      <div className="page-heading">
        <div><h2>{t('keys.title')}</h2><p>{t('keys.subtitle')}</p></div>
        <div className="page-actions">
          <Button onClick={openWizard} loading={wizardBusy && wizardStep === 0 && !wizardOpen}>{t('keys.chatgptLogin')}</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>{t('keys.create')}</Button>
        </div>
      </div>
      <Table<ApiKey>
        rowKey="id"
        loading={loading}
        dataSource={keys}
        scroll={{ x: 'max-content' }}
        pagination={{ hideOnSinglePage: true }}
        locale={{ emptyText: t('keys.empty') }}
        columns={[
          { title: t('common.name'), dataIndex: 'name' },
          {
            title: t('keys.kind'),
            width: 110,
            render: (_, k) =>
              isChatgpt(k) ? (
                <Tag color="geekblue">{t('keys.kindChatgpt')}{k.plan ? ` · ${k.plan}` : ''}</Tag>
              ) : (
                <Tag>{t('keys.kindApi')}</Tag>
              ),
          },
          {
            title: t('keys.account'),
            width: 220,
            render: (_, k) =>
              isChatgpt(k) ? (
                <Space size={4} wrap>
                  <Typography.Text code>{k.accountId || '—'}</Typography.Text>
                  {k.email && <Typography.Text type="secondary">{k.email}</Typography.Text>}
                  {/* access_token 到期属正常现象（下次使用自动刷新），不标红；刷新失败看状态列的 lastError */}
                  {k.expiresAt ? (
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {k.expiresAt < Date.now() / 1000
                        ? t('keys.tokenStale')
                        : `${t('keys.expiresAt')} ${formatDateTime(k.expiresAt)}`}
                    </Typography.Text>
                  ) : null}
                </Space>
              ) : (
                <span className="text-tertiary">—</span>
              ),
          },
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
            render: (v: string) => v || <span className="text-tertiary">—</span>,
          },
          { title: t('common.createdAt'), dataIndex: 'createdAt', width: 180, className: 'cell-time', render: (v: number | string) => formatDateTime(v) },
          {
            title: t('common.action'),
            fixed: 'right',
            width: hasChatgpt ? 280 : 200,
            render: (_, k) => (
              <Space>
                <Button size="small" onClick={() => openEdit(k)} title={isChatgpt(k) ? t('keys.chatgptNoEditValue') : undefined}>
                  {t('common.edit')}
                </Button>
                {isChatgpt(k) && (
                  <Button size="small" icon={<SyncOutlined />} onClick={() => doRefreshKey(k)}>
                    {t('keys.refresh')}
                  </Button>
                )}
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
          {!(editing && isChatgpt(editing)) && (
            <Form.Item name="value" label={editing ? t('keys.valueKeep') : t('keys.value')} extra={<span className="form-hint">{t('keys.valueHint')}</span>}>
              <Input.Password placeholder="sk-..." />
            </Form.Item>
          )}
          {editing && isChatgpt(editing) && <Alert type="info" showIcon message={t('keys.chatgptNoEditValue')} />}
          <Form.Item name="note" label={t('common.note')}>
            <Input.TextArea rows={2} />
          </Form.Item>
        </Form>
      </Modal>
      <Modal
        title={t('keys.chatgptWizardTitle')}
        open={wizardOpen}
        onCancel={() => setWizardOpen(false)}
        footer={
          wizardStep === 0
            ? [<Button key="cancel" onClick={() => setWizardOpen(false)}>{t('common.cancel')}</Button>,
               <Button key="next" type="primary" onClick={wizardContinue}>{t('keys.chatgptOpenAuth')}</Button>]
            : [<Button key="back" onClick={() => setWizardStep(0)}>{t('common.back')}</Button>,
               <Button key="ok" type="primary" loading={wizardBusy} onClick={wizardComplete}>{t('common.ok')}</Button>]
        }
        destroyOnClose
      >
        <Steps
          size="small"
          current={wizardStep}
          items={[{ title: t('keys.chatgptStepAuth') }, { title: t('keys.chatgptStepPaste') }]}
          style={{ marginBottom: 16 }}
        />
        {wizardStep === 0 ? (
          <>
            <Alert type="warning" showIcon message={t('keys.chatgptRisk')} style={{ marginBottom: 12 }} />
            <Typography.Paragraph type="secondary">{t('keys.chatgptStepAuthDesc')}</Typography.Paragraph>
          </>
        ) : (
          <Form layout="vertical">
            <Typography.Paragraph type="secondary">{t('keys.chatgptStepPasteDesc')}</Typography.Paragraph>
            <Form.Item label={t('keys.chatgptCallback')} htmlFor="chatgpt-callback" required>
              <Input.TextArea
                id="chatgpt-callback"
                rows={3}
                value={callbackUrl}
                onChange={(e) => setCallbackUrl(e.target.value)}
                placeholder={t('keys.chatgptCallbackPlaceholder')}
              />
            </Form.Item>
            <Form.Item label={t('common.name')} htmlFor="chatgpt-name" style={{ marginBottom: 0 }}>
              <Input
                id="chatgpt-name"
                value={accountName}
                onChange={(e) => setAccountName(e.target.value)}
                placeholder={t('keys.chatgptName')}
              />
            </Form.Item>
          </Form>
        )}
      </Modal>
    </div>
  )
}

export default KeysPage
