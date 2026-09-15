import React from 'react'
import {
  Table,
  Button,
  Modal,
  Form,
  Input,
  Select,
  Switch,
  InputNumber,
  Tag,
  message,
  Popconfirm,
  Space,
  Alert,
  Collapse,
  Spin,
} from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { Link, useParams } from 'react-router-dom'
import { listChannels, createChannel, updateChannel, deleteChannel, listKeys, testChannel, listCatalogModels } from '../api'
import type { ChannelTestResult } from '../api'
import type { Channel, ChannelInput, ApiKey, CatalogModel } from '../api/types'
import { fmtMs } from '../format'
import { useI18n } from '../i18n'

// 后端单组合探测总超时（probe.probeWait，秒）；矩阵并发执行，整体约等于单组合耗时
const PROBE_WAIT_SECONDS = 30

const emptyInput: ChannelInput = {
  name: '',
  type: '',
  baseUrls: [''],
  keyIds: [],
  keyStrategy: 'ordered',
  lineStrategy: 'auto',
  allowPublicProxy: false,
  models: [],
  modelMapping: {},
  forwardMode: 'passthrough',
  priority: 0,
  priceMultiplier: 1,
  pricingMode: 'usd',
  cnyRatio: 0,
  isDefault: false,
  enabled: true,
}

const ChannelsPage: React.FC = () => {
  const { t } = useI18n()
  const params = useParams()
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [keys, setKeys] = React.useState<ApiKey[]>([])
  const [catalog, setCatalog] = React.useState<CatalogModel[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<Channel | null>(null)
  const [testResults, setTestResults] = React.useState<ChannelTestResult[] | null>(null)
  const [testTarget, setTestTarget] = React.useState<Channel | null>(null)
  const [testing, setTesting] = React.useState(false)
  const testSeq = React.useRef(0)
  const [form] = Form.useForm()

  const refresh = React.useCallback(() => {
    setLoading(true)
    Promise.all([listChannels(), listKeys(), listCatalogModels()])
      .then(([c, k, m]) => {
        setChannels(c.channels)
        setKeys(k.keys)
        setCatalog(m.models)
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  React.useEffect(refresh, [refresh])

  // 点选数据源：管理员预置目录 ∪ 已有模型（去重）
  const modelOptions = React.useMemo(() => {
    const seen = new Set<string>()
    const out: { value: string; label: string }[] = []
    for (const m of catalog) {
      if (!seen.has(m.name)) {
        seen.add(m.name)
        out.push({ value: m.name, label: m.name })
      }
    }
    for (const c of channels) {
      for (const m of Array.isArray(c.models) ? c.models : []) {
        if (m && !seen.has(m)) {
          seen.add(m)
          out.push({ value: m, label: m })
        }
      }
    }
    return out
  }, [catalog, channels])

  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue(emptyInput)
    setModalOpen(true)
  }

  const openEdit = (c: Channel) => {
    setEditing(c)
    form.setFieldsValue({
      name: c.name,
      type: c.type,
      baseUrls: c.baseUrls,
      keyIds: c.keyIds,
      keyStrategy: c.keyStrategy,
      lineStrategy: c.lineStrategy,
      proxyUrl: '',
      allowPublicProxy: c.allowPublicProxy,
      models: c.models,
      modelMapping: Object.entries(c.modelMapping).map(([from, to]) => ({ from, to })),
      forwardMode: c.forwardMode ?? 'passthrough',
      priority: c.priority,
      priceMultiplier: c.priceMultiplier ?? 1,
      pricingMode: c.pricingMode ?? 'usd',
      cnyRatio: c.cnyRatio ?? 0,
      isDefault: c.isDefault,
      enabled: c.enabled,
    })
    setModalOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    const mapping: Record<string, string> = {}
    for (const { from, to } of v.modelMapping || []) {
      if (from && to) mapping[from] = to
    }
    const input: ChannelInput = {
      name: v.name,
      // 透明转发不需要协议类型；仅跨协议转换时提交所选目标协议
      type: v.forwardMode === 'convert' ? v.type : '',
      baseUrls: (v.baseUrls as string[]).filter(Boolean),
      keyIds: v.keyIds || [],
      keyStrategy: v.keyStrategy,
      lineStrategy: v.lineStrategy,
      proxyUrl: v.proxyUrl || undefined,
      allowPublicProxy: v.allowPublicProxy,
      models: v.models || [],
      modelMapping: mapping,
      forwardMode: v.forwardMode ?? 'passthrough',
      priority: v.priority,
      priceMultiplier: v.priceMultiplier || 1,
      pricingMode: v.pricingMode,
      cnyRatio: v.cnyRatio || 0,
      isDefault: v.isDefault,
      enabled: v.enabled,
    }
    try {
      if (editing) {
        await updateChannel(editing.id, input)
      } else {
        await createChannel(input)
      }
      message.success(t('channels.savedNextRequest'))
      setModalOpen(false)
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  // 测试：立即打开结果弹窗进入 loading 态，后端并发探测矩阵，返回后填充
  const runTest = async (c: Channel) => {
    const seq = ++testSeq.current
    setTestTarget(c)
    setTesting(true)
    setTestResults([])
    try {
      const r = await testChannel(c.id)
      if (testSeq.current !== seq) return // 期间已关闭弹窗，丢弃过期响应
      setTestResults(r.results)
    } catch (e) {
      if (testSeq.current !== seq) return
      setTestResults(null)
      message.error((e as Error).message)
    } finally {
      if (testSeq.current === seq) setTesting(false)
    }
  }

  // 渠道对象 → 更新入参（不传 proxyUrl，后端保持原代理不变）
  const channelToInput = (c: Channel, overrides: Partial<ChannelInput>): ChannelInput => ({
    name: c.name,
    type: c.forwardMode === 'convert' ? c.type : '',
    baseUrls: c.baseUrls,
    keyIds: c.keyIds,
    keyStrategy: c.keyStrategy,
    lineStrategy: c.lineStrategy,
    allowPublicProxy: c.allowPublicProxy,
    models: Array.isArray(c.models) ? c.models : [],
    modelMapping: c.modelMapping ?? {},
    forwardMode: c.forwardMode,
    priority: c.priority,
    priceMultiplier: c.priceMultiplier ?? 1,
    pricingMode: c.pricingMode ?? 'usd',
    cnyRatio: c.cnyRatio ?? 0,
    isDefault: c.isDefault,
    enabled: c.enabled,
    ...overrides,
  })

  const toggleEnabled = async (c: Channel) => {
    try {
      await updateChannel(c.id, channelToInput(c, { enabled: !c.enabled }))
      message.success(c.enabled ? t('channels.disabledNotice') : t('channels.enabledNextRequest'))
      refresh()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  // 从模板复制等入口带 /channels/:id 跳转过来时，加载完成后自动打开该渠道编辑
  const openedRouteId = React.useRef<string | undefined>(undefined)
  React.useEffect(() => {
    const id = params.id
    if (!id || id === openedRouteId.current || loading || !channels.length) return
    const target = channels.find((c) => String(c.id) === id)
    if (!target) return
    openedRouteId.current = id
    openEdit(target)
  }, [params.id, channels, loading])

  return (
    <div>
      <div className="page-heading">
        <div>
          <h2>{t('channels.title')}</h2>
          <p>
            {t('channels.subtitleLead')}
            <Link to="/templates">{t('channels.templatesLink')}</Link>
            {t('channels.subtitleTail')}
          </p>
        </div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>{t('channels.create')}</Button></div>
      </div>
      <Table<Channel>
        rowKey="id"
        loading={loading}
        dataSource={channels}
        scroll={{ x: 'max-content' }}
        locale={{ emptyText: t('channels.empty') }}
        columns={[
          {
            title: t('common.name'),
            dataIndex: 'name',
            render: (n: string, c: Channel) => (
              <Space>
                {n}
                {c.copiedFromTemplateId ? <Tag>{t('channels.copiedFromTemplate')}</Tag> : null}
                {c.isDefault ? <Tag color="blue">{t('channels.defaultTag')}</Tag> : null}
              </Space>
            ),
          },
          {
            title: t('channels.forward'),
            dataIndex: 'forwardMode',
            width: 120,
            render: (m: string, c: Channel) =>
              m === 'convert' ? (
                <Tag color="orange">{t('channels.convertTo', { type: c.type || '?' })}</Tag>
              ) : (
                <Tag>{t('channels.transparent')}</Tag>
              ),
          },
          { title: t('channels.lineCount'), width: 90, align: 'right', render: (_, c) => c.baseUrls.length },
          { title: t('channels.keyCount'), width: 90, align: 'right', render: (_, c) => c.keyIds.length },
          { title: t('channels.priority'), dataIndex: 'priority', width: 90, align: 'right' },
          {
            title: t('common.status'),
            dataIndex: 'enabled',
            width: 90,
            render: (e: boolean, c: Channel) =>
              e ? (
                <Tag color="green">{t('common.enabled')}</Tag>
              ) : c.keyIds.length === 0 ? (
                <Tag color="orange">{t('channels.draft')}</Tag>
              ) : (
                <Tag>{t('common.disabled')}</Tag>
              ),
          },
          {
            title: t('common.action'),
            width: 250,
            render: (_, c) => (
              <Space>
                <Button size="small" onClick={() => openEdit(c)}>{t('common.edit')}</Button>
                <Button size="small" onClick={() => toggleEnabled(c)}>{c.enabled ? t('common.disabled') : t('common.enabled')}</Button>
                <Button size="small" loading={testing && testTarget?.id === c.id} onClick={() => runTest(c)}>{t('channels.test')}</Button>
                <Popconfirm
                  title={t('channels.confirmDelete')}
                  onConfirm={async () => {
                    await deleteChannel(c.id)
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
        title={editing ? t('channels.editTitle', { name: editing.name }) : t('channels.createTitle')}
        open={modalOpen}
        onOk={submit}
        onCancel={() => setModalOpen(false)}
        width={680}
        destroyOnClose
      >
        <Form form={form} layout="vertical" initialValues={emptyInput}>
          <Form.Item name="name" label={t('common.name')} tooltip={t('channels.nameTooltip')} rules={[{ required: true, message: t('common.nameRequired') }]}>
            <Input placeholder={t('channels.namePlaceholder')} />
          </Form.Item>
          <Form.Item name="enabled" label={t('channels.enableChannel')} valuePropName="checked" extra={<span className="form-hint">{t('channels.enableChannelExtra')}</span>}>
            <Switch />
          </Form.Item>
          <Form.Item label={t('channels.baseUrlLabel')} extra={<span className="form-hint">{t('channels.baseUrlExtra')}</span>} required>
            <Form.List name="baseUrls">
              {(fields, { add, remove }) => (
                <>
                  {fields.map((f) => (
                    <Space key={f.key} style={{ display: 'flex', marginBottom: 4 }}>
                      <Form.Item name={[f.name]} noStyle rules={[{ required: true, message: t('channels.baseUrlRequired') }]}>
                        <Input placeholder="https://api.example.com" style={{ width: 420 }} />
                      </Form.Item>
                      {fields.length > 1 ? <a onClick={() => remove(f.name)}>{t('common.delete')}</a> : null}
                    </Space>
                  ))}
                  {fields.length < 5 ? (
                    <Button type="dashed" onClick={() => add('')} block>
                      {t('channels.addLine')}
                    </Button>
                  ) : null}
                </>
              )}
            </Form.List>
          </Form.Item>
          <Form.Item name="keyIds" label={t('channels.bindKeys')} extra={<span className="form-hint">{t('channels.bindKeysExtra')}</span>}>
            <Select
              mode="multiple"
              options={keys.map((k) => ({ value: k.id, label: k.name }))}
              placeholder={t('channels.selectFromKeyPool')}
            />
          </Form.Item>
          <Space size="large">
            <Form.Item name="keyStrategy" label={t('channels.keyStrategy')}>
              <Select
                style={{ width: 160 }}
                options={[
                  { value: 'ordered', label: t('channels.keyStrategyOrdered') },
                  { value: 'round_robin', label: t('channels.keyStrategyRoundRobin') },
                ]}
              />
            </Form.Item>
            <Form.Item name="lineStrategy" label={t('channels.lineStrategy')}>
              <Select
                style={{ width: 160 }}
                options={[
                  { value: 'auto', label: t('channels.lineStrategyAuto') },
                  { value: 'manual', label: t('channels.lineStrategyManual') },
                ]}
              />
            </Form.Item>
          </Space>
          <Form.Item name="models" label={t('channels.models')} extra={t('channels.modelsExtra')}>
            <Select mode="tags" tokenSeparators={[',']} options={modelOptions} placeholder={t('channels.modelsPlaceholder')} />
          </Form.Item>
          <Collapse ghost defaultActiveKey={[]} style={{ marginTop: 4, marginBottom: 8 }}>
            <Collapse.Panel header={t('channels.advanced')} key="advanced">
          <Form.Item name="forwardMode" label={t('channels.forwardMode')} extra={t('channels.forwardModeExtra')}>
            <Select
              style={{ width: 260 }}
              options={[
                { value: 'passthrough', label: t('channels.forwardPassthrough') },
                { value: 'convert', label: t('channels.forwardConvert') },
              ]}
            />
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(a, b) => a.forwardMode !== b.forwardMode}>
            {({ getFieldValue }) =>
              getFieldValue('forwardMode') === 'convert' ? (
                <Form.Item
                  name="type"
                  label={t('channels.targetProtocol')}
                  tooltip={t('channels.targetProtocolTooltip')}
                  rules={[{ required: true, message: t('channels.targetProtocolRequired') }]}
                >
                  <Select
                    style={{ width: 260 }}
                    options={[
                      { value: 'openai', label: 'openai' },
                      { value: 'anthropic', label: 'anthropic' },
                    ]}
                  />
                </Form.Item>
              ) : null
            }
          </Form.Item>
          <Form.Item name="proxyUrl" label={t('channels.personalProxy')} extra={<span className="form-hint">{t('channels.personalProxyExtra')}</span>}>
            <Input placeholder="socks5://127.0.0.1:7890" />
          </Form.Item>
          <Form.Item name="allowPublicProxy" label={t('channels.allowPublicProxy')} valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item label={t('channels.modelMapping')}>
            <Form.List name="modelMapping">
              {(fields, { add, remove }) => (
                <>
                  {fields.map((f) => (
                    <Space key={f.key} style={{ display: 'flex', marginBottom: 4 }}>
                      <Form.Item name={[f.name, 'from']} noStyle>
                        <Input placeholder={t('channels.mappingFromPlaceholder')} style={{ width: 200 }} />
                      </Form.Item>
                      <span>→</span>
                      <Form.Item name={[f.name, 'to']} noStyle>
                        <Input placeholder={t('channels.mappingToPlaceholder')} style={{ width: 200 }} />
                      </Form.Item>
                      <a onClick={() => remove(f.name)}>{t('common.delete')}</a>
                    </Space>
                  ))}
                  <Button type="dashed" onClick={() => add({ from: '', to: '' })} block>
                    {t('channels.addMapping')}
                  </Button>
                </>
              )}
            </Form.List>
          </Form.Item>
          <Space size="large">
            <Form.Item name="priority" label={t('channels.priorityLabel')}>
              <InputNumber />
            </Form.Item>
            <Form.Item name="pricingMode" label={t('channels.pricingMode')}>
              <Select
                style={{ width: 190 }}
                options={[
                  { value: 'usd', label: t('channels.pricingUsd') },
                  { value: 'cny_ratio', label: t('channels.pricingCnyRatio') },
                ]}
              />
            </Form.Item>
          </Space>
          <Form.Item noStyle shouldUpdate={(a, b) => a.pricingMode !== b.pricingMode}>
            {({ getFieldValue }) =>
              getFieldValue('pricingMode') === 'cny_ratio' ? (
                <Form.Item
                  name="cnyRatio"
                  label={t('channels.cnyRatioLabel')}
                  extra={t('channels.cnyRatioExtra')}
                  rules={[{ required: true, message: t('channels.cnyRatioRequired') }]}
                >
                  <InputNumber min={0.001} step={0.05} style={{ width: 160 }} />
                </Form.Item>
              ) : (
                <Form.Item
                  name="priceMultiplier"
                  label={t('channels.priceMultiplierLabel')}
                  extra={t('channels.priceMultiplierExtra')}
                  initialValue={1}
                >
                  <InputNumber min={0} step={0.05} style={{ width: 160 }} />
                </Form.Item>
              )
            }
          </Form.Item>
          <Form.Item name="isDefault" label={t('channels.setDefault')} valuePropName="checked">
            <Switch />
          </Form.Item>
            </Collapse.Panel>
          </Collapse>
        </Form>
      </Modal>

      <Modal
        title={testTarget ? t('channels.testResultTitleWithName', { name: testTarget.name }) : t('channels.testResultTitle')}
        open={testResults !== null}
        onCancel={() => {
          testSeq.current++ // 丢弃仍在途的探测响应
          setTesting(false)
          setTestResults(null)
        }}
        footer={null}
      >
        {testing ? (
          <div style={{ textAlign: 'center', padding: '32px 0' }}>
            <Spin />
            <div className="text-secondary" style={{ marginTop: 12 }}>
              {t('channels.probing', { seconds: PROBE_WAIT_SECONDS })}
            </div>
          </div>
        ) : (
          testResults?.map((r, i) => (
            <Alert
              key={i}
              style={{ marginBottom: 8 }}
              type={r.ok ? 'success' : 'error'}
              message={r.lineUrl}
              description={
                <>
                  <div>{t('channels.testDetail', { via: r.via, latency: fmtMs(r.latencyMs) })}</div>
                  {r.error ? <div className="text-secondary">{r.error}</div> : null}
                </>
              }
            />
          ))
        )}
      </Modal>
    </div>
  )
}

export default ChannelsPage
