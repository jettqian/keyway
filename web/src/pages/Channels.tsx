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
      message.success('已保存，下一个请求生效')
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
      message.success(c.enabled ? '已停用' : '已启用，下一个请求生效')
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
          <h2>渠道</h2>
          <p>管理上游线路、密钥和模型路由，保存后下一个请求即可生效；没有头绪可先从<Link to="/templates">预制模板</Link>复制。</p>
        </div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建渠道</Button></div>
      </div>
      <Table<Channel>
        rowKey="id"
        loading={loading}
        dataSource={channels}
        locale={{ emptyText: '暂无渠道，点击右上角「新建渠道」或从预制模板复制' }}
        columns={[
          {
            title: '名称',
            dataIndex: 'name',
            render: (n: string, c: Channel) => (
              <Space>
                {n}
                {c.copiedFromTemplateId ? <Tag>复制自模板</Tag> : null}
                {c.isDefault ? <Tag color="blue">默认</Tag> : null}
              </Space>
            ),
          },
          {
            title: '转发',
            dataIndex: 'forwardMode',
            width: 120,
            render: (m: string, c: Channel) =>
              m === 'convert' ? (
                <Tag color="orange">转换 → {c.type || '?'}</Tag>
              ) : (
                <Tag>透明</Tag>
              ),
          },
          { title: '线路数', width: 90, align: 'right', render: (_, c) => c.baseUrls.length },
          { title: '密钥数', width: 90, align: 'right', render: (_, c) => c.keyIds.length },
          { title: '优先级', dataIndex: 'priority', width: 90, align: 'right' },
          {
            title: '状态',
            dataIndex: 'enabled',
            width: 90,
            render: (e: boolean, c: Channel) =>
              e ? (
                <Tag color="green">启用</Tag>
              ) : c.keyIds.length === 0 ? (
                <Tag color="orange">草稿</Tag>
              ) : (
                <Tag>停用</Tag>
              ),
          },
          {
            title: '操作',
            width: 250,
            render: (_, c) => (
              <Space>
                <Button size="small" onClick={() => openEdit(c)}>编辑</Button>
                <Button size="small" onClick={() => toggleEnabled(c)}>{c.enabled ? '停用' : '启用'}</Button>
                <Button size="small" loading={testing && testTarget?.id === c.id} onClick={() => runTest(c)}>测试</Button>
                <Popconfirm
                  title="删除该渠道？"
                  onConfirm={async () => {
                    await deleteChannel(c.id)
                    message.success('已删除')
                    refresh()
                  }}
                >
                  <Button size="small" danger>删除</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />

      <Modal
        title={editing ? `编辑渠道：${editing.name}` : '新建渠道'}
        open={modalOpen}
        onOk={submit}
        onCancel={() => setModalOpen(false)}
        width={680}
        destroyOnClose
      >
        <Form form={form} layout="vertical" initialValues={emptyInput}>
          <Form.Item name="name" label="名称" tooltip="给自己看的标识，建议包含供应商或用途" rules={[{ required: true, message: '请输入名称' }]}>
            <Input placeholder="如 openai-官方" />
          </Form.Item>
          <Form.Item name="enabled" label="启用渠道" valuePropName="checked" extra={<span className="form-hint">停用（草稿）状态不参与任何路由；启用须至少绑定 1 把密钥，保存后下一个请求生效。</span>}>
            <Switch />
          </Form.Item>
          <Form.Item label="线路地址" extra={<span className="form-hint">按优先顺序填写，最多 5 条；系统会自动选择可用线路。</span>} required>
            <Form.List name="baseUrls">
              {(fields, { add, remove }) => (
                <>
                  {fields.map((f) => (
                    <Space key={f.key} style={{ display: 'flex', marginBottom: 4 }}>
                      <Form.Item name={[f.name]} noStyle rules={[{ required: true, message: '线路不能为空' }]}>
                        <Input placeholder="https://api.example.com" style={{ width: 420 }} />
                      </Form.Item>
                      {fields.length > 1 ? <a onClick={() => remove(f.name)}>删除</a> : null}
                    </Space>
                  ))}
                  {fields.length < 5 ? (
                    <Button type="dashed" onClick={() => add('')} block>
                      添加线路
                    </Button>
                  ) : null}
                </>
              )}
            </Form.List>
          </Form.Item>
          <Form.Item name="keyIds" label="绑定密钥" extra={<span className="form-hint">留空表示暂不绑定密钥；最多 5 个，按所选策略使用。</span>}>
            <Select
              mode="multiple"
              options={keys.map((k) => ({ value: k.id, label: k.name }))}
              placeholder="从密钥池选择"
            />
          </Form.Item>
          <Space size="large">
            <Form.Item name="keyStrategy" label="密钥策略">
              <Select
                style={{ width: 160 }}
                options={[
                  { value: 'ordered', label: 'ordered（顺序优先）' },
                  { value: 'round_robin', label: 'round_robin（轮询）' },
                ]}
              />
            </Form.Item>
            <Form.Item name="lineStrategy" label="线路策略">
              <Select
                style={{ width: 160 }}
                options={[
                  { value: 'auto', label: 'auto（探测优选）' },
                  { value: 'manual', label: 'manual（固定第一条）' },
                ]}
              />
            </Form.Item>
          </Space>
          <Form.Item name="models" label="模型列表" extra="从模型目录或已有模型中点选；目录外的名称可直接输入回车添加。">
            <Select mode="tags" tokenSeparators={[',']} options={modelOptions} placeholder="点选或输入模型名" />
          </Form.Item>
          <Collapse ghost defaultActiveKey={[]} style={{ marginTop: 4, marginBottom: 8 }}>
            <Collapse.Panel header="高级选项" key="advanced">
          <Form.Item name="forwardMode" label="转发模式" extra="默认透明转发：沿用入站协议，无需选择协议类型；跨协议转换为特殊需求，显式开启。">
            <Select
              style={{ width: 260 }}
              options={[
                { value: 'passthrough', label: '透明转发（默认）' },
                { value: 'convert', label: '跨协议转换（高级）' },
              ]}
            />
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(a, b) => a.forwardMode !== b.forwardMode}>
            {({ getFieldValue }) =>
              getFieldValue('forwardMode') === 'convert' ? (
                <Form.Item
                  name="type"
                  label="目标协议"
                  tooltip="跨协议转换时上游使用的协议；透明转发模式下不涉及此项。"
                  rules={[{ required: true, message: '跨协议转换须指定目标协议' }]}
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
          <Form.Item name="proxyUrl" label="个人出站代理" extra={<span className="form-hint">支持 http 或 socks5；不需要代理时留空。</span>}>
            <Input placeholder="socks5://127.0.0.1:7890" />
          </Form.Item>
          <Form.Item name="allowPublicProxy" label="允许使用公共代理参与优选" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item label="模型映射（请求名 → 上游名）">
            <Form.List name="modelMapping">
              {(fields, { add, remove }) => (
                <>
                  {fields.map((f) => (
                    <Space key={f.key} style={{ display: 'flex', marginBottom: 4 }}>
                      <Form.Item name={[f.name, 'from']} noStyle>
                        <Input placeholder="请求模型名" style={{ width: 200 }} />
                      </Form.Item>
                      <span>→</span>
                      <Form.Item name={[f.name, 'to']} noStyle>
                        <Input placeholder="上游模型名" style={{ width: 200 }} />
                      </Form.Item>
                      <a onClick={() => remove(f.name)}>删除</a>
                    </Space>
                  ))}
                  <Button type="dashed" onClick={() => add({ from: '', to: '' })} block>
                    添加映射
                  </Button>
                </>
              )}
            </Form.List>
          </Form.Item>
          <Space size="large">
            <Form.Item name="priority" label="优先级（大者优先）">
              <InputNumber />
            </Form.Item>
            <Form.Item name="pricingMode" label="计价模式（费用统计用）">
              <Select
                style={{ width: 190 }}
                options={[
                  { value: 'usd', label: '美元渠道（倍率折扣）' },
                  { value: 'cny_ratio', label: '人民币渠道（$1 实收 ¥X）' },
                ]}
              />
            </Form.Item>
          </Space>
          <Form.Item noStyle shouldUpdate={(a, b) => a.pricingMode !== b.pricingMode}>
            {({ getFieldValue }) =>
              getFieldValue('pricingMode') === 'cny_ratio' ? (
                <Form.Item
                  name="cnyRatio"
                  label="换算比（每 $1 官方用量实收人民币）"
                  extra="如 micu 渠道 $1 收 ¥0.5 就填 0.5；统计按全局汇率折算美元"
                  rules={[{ required: true, message: '人民币渠道须填写换算比' }]}
                >
                  <InputNumber min={0.001} step={0.05} style={{ width: 160 }} />
                </Form.Item>
              ) : (
                <Form.Item
                  name="priceMultiplier"
                  label="价格倍率（费用统计用）"
                  extra="美元渠道折扣，如 8 折填 0.8；官方渠道保持 1"
                  initialValue={1}
                >
                  <InputNumber min={0} step={0.05} style={{ width: 160 }} />
                </Form.Item>
              )
            }
          </Form.Item>
          <Form.Item name="isDefault" label="设为默认渠道（模型未命中任何渠道时兜底）" valuePropName="checked">
            <Switch />
          </Form.Item>
            </Collapse.Panel>
          </Collapse>
        </Form>
      </Modal>

      <Modal
        title={`线路 × 路径 测试结果${testTarget ? `：${testTarget.name}` : ''}`}
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
              正在并发探测全部线路 × 路径组合（每组合最长 {PROBE_WAIT_SECONDS} 秒）…
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
                  <div>路径 {r.via}，延迟 {fmtMs(r.latencyMs)}</div>
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
