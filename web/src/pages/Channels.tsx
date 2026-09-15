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
} from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { listChannels, createChannel, updateChannel, deleteChannel, listKeys, testChannel } from '../api'
import type { ChannelTestResult } from '../api'
import type { Channel, ChannelInput, ApiKey } from '../api/types'

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
  const nav = useNavigate()
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [keys, setKeys] = React.useState<ApiKey[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<Channel | null>(null)
  const [testResults, setTestResults] = React.useState<ChannelTestResult[] | null>(null)
  const [form] = Form.useForm()

  const refresh = React.useCallback(() => {
    setLoading(true)
    Promise.all([listChannels(), listKeys()])
      .then(([c, k]) => {
        setChannels(c.channels)
        setKeys(k.keys)
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  React.useEffect(refresh, [refresh])

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

  const runTest = async (c: Channel) => {
    try {
      const r = await testChannel(c.id)
      setTestResults(r.results)
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <div className="page-heading">
        <div><h2>渠道</h2><p>管理上游线路、密钥和模型路由。保存后下一个请求即可生效。</p></div>
        <div className="page-actions"><Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建渠道</Button></div>
      </div>
      <Table<Channel>
        rowKey="id"
        loading={loading}
        dataSource={channels}
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
          { title: '线路数', width: 90, render: (_, c) => c.baseUrls.length },
          { title: '密钥数', width: 90, render: (_, c) => c.keyIds.length },
          { title: '优先级', dataIndex: 'priority', width: 90 },
          {
            title: '状态',
            dataIndex: 'enabled',
            width: 90,
            render: (e: boolean) => (e ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>),
          },
          {
            title: '操作',
            width: 220,
            render: (_, c) => (
              <Space>
                <a onClick={() => openEdit(c)}>编辑</a>
                <a onClick={() => runTest(c)}>测试</a>
                <Popconfirm
                  title="删除该渠道？"
                  onConfirm={async () => {
                    await deleteChannel(c.id)
                    message.success('已删除')
                    refresh()
                  }}
                >
                  <a style={{ color: 'red' }}>删除</a>
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
          <Form.Item name="models" label="模型列表" extra="基础配置：一个渠道可以绑定多个模型；也可在模型管理页批量维护。">
            <Select mode="tags" placeholder="该渠道服务的模型名，回车添加" />
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
          <Space size="large">
            <Form.Item name="isDefault" label="设为默认渠道" valuePropName="checked">
              <Switch />
            </Form.Item>
            <Form.Item name="enabled" label="启用" valuePropName="checked">
              <Switch />
            </Form.Item>
          </Space>
            </Collapse.Panel>
          </Collapse>
        </Form>
      </Modal>

      <Modal
        title={`线路 × 路径 测试结果${editing ? `：${editing.name}` : ''}`}
        open={testResults !== null}
        onCancel={() => setTestResults(null)}
        footer={null}
      >
        {testResults?.map((r, i) => (
          <Alert
            key={i}
            style={{ marginBottom: 8 }}
            type={r.ok ? 'success' : 'error'}
            message={`${r.lineUrl} · ${r.via} · ${r.latencyMs}ms`}
            description={r.error || undefined}
          />
        ))}
      </Modal>
      <Button type="link" onClick={() => nav('/templates')} style={{ paddingLeft: 0 }}>
        查看预制模板，一键复制接入 →
      </Button>
    </div>
  )
}

export default ChannelsPage
