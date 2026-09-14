import React from 'react'
import { Tabs, Table, Form, Switch, Input, Button, message, Modal, Tag, Space, Select, InputNumber, Typography } from 'antd'
import {
  adminUsers,
  adminSetUserStatus,
  adminResetPassword,
  adminPricing,
  adminProxies,
  adminCreateProxy,
  adminUpdateProxy,
  adminDeleteProxy,
  adminTemplates,
  adminCreateTemplate,
  adminUpdateTemplate,
  adminDeleteTemplate,
  adminSettings,
  adminUpdateSettings,
  adminStats,
} from '../api'
import type { User, ModelPricing, Proxy, ChannelTemplate, StatsResponse, StatsGroup } from '../api/types'

const UsersTab: React.FC = () => {
  const [users, setUsers] = React.useState<User[]>([])
  const [loading, setLoading] = React.useState(true)
  const refresh = React.useCallback(() => {
    setLoading(true)
    adminUsers()
      .then((r) => setUsers(r.users))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  React.useEffect(refresh, [refresh])
  return (
    <Table<User>
      rowKey="id"
      loading={loading}
      dataSource={users}
      columns={[
        { title: 'ID', dataIndex: 'id', width: 60 },
        { title: '用户名', dataIndex: 'username' },
        { title: '角色', dataIndex: 'role', render: (r: number) => (r === 100 ? <Tag color="red">管理员</Tag> : <Tag>用户</Tag>) },
        {
          title: '状态',
          dataIndex: 'status',
          render: (s: number) => (s === 1 ? <Tag color="green">正常</Tag> : <Tag color="orange">禁用</Tag>),
        },
        { title: '注册时间', dataIndex: 'createdAt', width: 180 },
        { title: '最近登录', dataIndex: 'lastLoginAt', width: 180 },
        {
          title: '操作',
          render: (_, u) => (
            <Space>
              <a
                onClick={async () => {
                  await adminSetUserStatus(u.id, u.status === 1 ? 2 : 1)
                  message.success('已更新')
                  refresh()
                }}
              >
                {u.status === 1 ? '禁用' : '启用'}
              </a>
              <a
                onClick={async () => {
                  const r = await adminResetPassword(u.id)
                  Modal.info({ title: '新密码', content: <Typography.Text copyable>{r.password}</Typography.Text> })
                }}
              >
                重置密码
              </a>
            </Space>
          ),
        },
      ]}
    />
  )
}

// UsersTab 中使用 Modal 展示重置密码结果

const PricingTab: React.FC = () => {
  const [pricing, setPricing] = React.useState<ModelPricing[]>([])
  const [loading, setLoading] = React.useState(true)
  const refresh = React.useCallback(() => {
    setLoading(true)
    adminPricing()
      .then((r) => setPricing(r.pricing))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  React.useEffect(refresh, [refresh])
  return (
    <Table<ModelPricing>
      rowKey="model"
      loading={loading}
      dataSource={pricing}
      pagination={{ pageSize: 20 }}
      columns={[
        { title: '模型', dataIndex: 'model' },
        { title: '输入 $/M', dataIndex: 'inputPerM' },
        { title: '缓存读 $/M', dataIndex: 'cachedInputPerM', render: (v: number | null) => v ?? '（同输入价）' },
        { title: '缓存写 $/M', dataIndex: 'cacheWritePerM', render: (v: number | null) => v ?? '（同输入价）' },
        { title: '输出 $/M', dataIndex: 'outputPerM' },
      ]}
    />
  )
}

const ProxiesTab: React.FC = () => {
  const [proxies, setProxies] = React.useState<Proxy[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<Proxy | null>(null)
  const [form] = Form.useForm()
  const refresh = React.useCallback(() => {
    setLoading(true)
    adminProxies()
      .then((r) => setProxies(r.proxies))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  React.useEffect(refresh, [refresh])
  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ name: '', url: '', note: '', enabled: true })
    setModalOpen(true)
  }
  const openEdit = (p: Proxy) => {
    setEditing(p)
    form.setFieldsValue({ name: p.name, url: '', note: p.note, enabled: p.enabled })
    setModalOpen(true)
  }
  const submit = async () => {
    const v = await form.validateFields()
    try {
      if (editing) {
        await adminUpdateProxy(editing.id, v)
        message.success('已更新，缓存即时生效')
      } else {
        await adminCreateProxy(v)
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
      <div style={{ marginBottom: 12, textAlign: 'right' }}>
        <Button type="primary" onClick={openCreate}>新建公共代理</Button>
      </div>
      <Table<Proxy>
        rowKey="id"
        loading={loading}
        dataSource={proxies}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '状态', dataIndex: 'enabled', render: (v: boolean) => (v ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>) },
          { title: '备注', dataIndex: 'note' },
          {
            title: '操作',
            width: 130,
            render: (_, p) => (
              <Space>
                <a onClick={() => openEdit(p)}>编辑</a>
                <a
                  style={{ color: 'red' }}
                  onClick={async () => {
                    await adminDeleteProxy(p.id)
                    message.success('已删除')
                    refresh()
                  }}
                >
                  删除
                </a>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={editing ? `编辑代理：${editing.name}` : '新建公共代理'} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input placeholder="如 mihomo-出口A" />
          </Form.Item>
          <Form.Item name="url" label={editing ? '代理地址（留空不修改）' : '代理地址'}>
            <Input placeholder="socks5://127.0.0.1:7890 或 http://host:port" />
          </Form.Item>
          <Form.Item name="note" label="备注">
            <Input />
          </Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

const TemplatesTab: React.FC = () => {
  const [templates, setTemplates] = React.useState<ChannelTemplate[]>([])
  const [loading, setLoading] = React.useState(true)
  const [modalOpen, setModalOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<ChannelTemplate | null>(null)
  const [form] = Form.useForm()
  const refresh = React.useCallback(() => {
    setLoading(true)
    adminTemplates()
      .then((r) => setTemplates(r.templates))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  React.useEffect(refresh, [refresh])
  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ type: 'openai', baseUrls: [''], models: [], lineStrategy: 'auto', priorityDefault: 0, allowPublicProxyDefault: false, note: '', enabled: true })
    setModalOpen(true)
  }
  const openEdit = (t: ChannelTemplate) => {
    setEditing(t)
    form.setFieldsValue({
      name: t.name, type: t.type, baseUrls: t.baseUrls, lineStrategy: t.lineStrategy,
      models: t.models, modelMapping: Object.entries(t.modelMapping).map(([from, to]) => ({ from, to })),
      priorityDefault: t.priorityDefault, allowPublicProxyDefault: t.allowPublicProxyDefault,
      note: t.note, enabled: t.enabled,
    })
    setModalOpen(true)
  }
  const submit = async () => {
    const v = await form.validateFields()
    const mapping: Record<string, string> = {}
    for (const { from, to } of v.modelMapping || []) {
      if (from && to) mapping[from] = to
    }
    const input = {
      name: v.name, type: v.type, baseUrls: (v.baseUrls as string[]).filter(Boolean),
      lineStrategy: v.lineStrategy, models: v.models || [], modelMapping: mapping,
      priorityDefault: v.priorityDefault, allowPublicProxyDefault: v.allowPublicProxyDefault,
      note: v.note, enabled: v.enabled,
    }
    try {
      if (editing) {
        await adminUpdateTemplate(editing.id, input)
        message.success('已更新')
      } else {
        await adminCreateTemplate(input)
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
      <div style={{ marginBottom: 12, textAlign: 'right' }}>
        <Button type="primary" onClick={openCreate}>新建模板</Button>
      </div>
      <Table<ChannelTemplate>
        rowKey="id"
        loading={loading}
        dataSource={templates}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '类型', dataIndex: 'type' },
          { title: '线路数', render: (_, t) => t.baseUrls.length },
          { title: '复制次数', dataIndex: 'copyCount' },
          { title: '说明', dataIndex: 'note', ellipsis: true },
          {
            title: '状态',
            dataIndex: 'enabled',
            render: (v: boolean) => (v ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>),
          },
          {
            title: '操作',
            width: 130,
            render: (_, t) => (
              <Space>
                <a onClick={() => openEdit(t)}>编辑</a>
                <a
                  style={{ color: 'red' }}
                  onClick={async () => {
                    await adminDeleteTemplate(t.id)
                    message.success('已删除（已复制渠道不受影响）')
                    refresh()
                  }}
                >
                  删除
                </a>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={editing ? `编辑模板：${editing.name}` : '新建预制模板'} open={modalOpen} onOk={submit} onCancel={() => setModalOpen(false)} width={640} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input />
          </Form.Item>
          <Form.Item name="type" label="协议类型" rules={[{ required: true }]}>
            <Select
              options={[
                { value: 'openai', label: 'openai（OpenAI 兼容）' },
                { value: 'anthropic', label: 'anthropic（Anthropic 兼容）' },
              ]}
            />
          </Form.Item>
          <Form.Item label="线路（base_url，按优先顺序，≤5 条）" required>
            <Form.List name="baseUrls">
              {(fields, { add, remove }) => (
                <>
                  {fields.map((f) => (
                    <Space key={f.key} style={{ display: 'flex', marginBottom: 4 }}>
                      <Form.Item name={[f.name]} noStyle rules={[{ required: true, message: '线路不能为空' }]}>
                        <Input placeholder="https://api.example.com" style={{ width: 400 }} />
                      </Form.Item>
                      {fields.length > 1 ? <a onClick={() => remove(f.name)}>删除</a> : null}
                    </Space>
                  ))}
                  {fields.length < 5 ? (
                    <Button type="dashed" onClick={() => add('')} block>添加线路</Button>
                  ) : null}
                </>
              )}
            </Form.List>
          </Form.Item>
          <Form.Item name="models" label="模型列表" rules={[{ required: true, message: '至少一个模型' }]}>
            <Select mode="tags" placeholder="回车添加" />
          </Form.Item>
          <Space size="large">
            <Form.Item name="lineStrategy" label="线路策略">
              <Select
                style={{ width: 150 }}
                options={[
                  { value: 'auto', label: 'auto（探测优选）' },
                  { value: 'manual', label: 'manual（固定第一条）' },
                ]}
              />
            </Form.Item>
            <Form.Item name="priorityDefault" label="默认优先级">
              <InputNumber />
            </Form.Item>
            <Form.Item name="allowPublicProxyDefault" label="默认允许公共代理" valuePropName="checked">
              <Switch />
            </Form.Item>
          </Space>
          <Form.Item label="模型映射（请求名 → 上游名）">
            <Form.List name="modelMapping">
              {(fields, { add, remove }) => (
                <>
                  {fields.map((f) => (
                    <Space key={f.key} style={{ display: 'flex', marginBottom: 4 }}>
                      <Form.Item name={[f.name, 'from']} noStyle>
                        <Input placeholder="请求模型名" style={{ width: 180 }} />
                      </Form.Item>
                      <span>→</span>
                      <Form.Item name={[f.name, 'to']} noStyle>
                        <Input placeholder="上游模型名" style={{ width: 180 }} />
                      </Form.Item>
                      <a onClick={() => remove(f.name)}>删除</a>
                    </Space>
                  ))}
                  <Button type="dashed" onClick={() => add({ from: '', to: '' })} block>添加映射</Button>
                </>
              )}
            </Form.List>
          </Form.Item>
          <Form.Item name="note" label="说明">
            <Input.TextArea rows={2} />
          </Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

const StatsTab: React.FC = () => {
  const [data, setData] = React.useState<StatsResponse | null>(null)
  const [loading, setLoading] = React.useState(true)
  React.useEffect(() => {
    adminStats(30)
      .then(setData)
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  return (
    <Table<StatsGroup>
      rowKey="dim"
      loading={loading}
      dataSource={data?.byModel ?? []}
      columns={[
        { title: '模型', dataIndex: 'dim' },
        { title: '请求数', dataIndex: 'requests' },
        { title: '输入 tokens', dataIndex: 'promptTokens' },
        { title: '输出 tokens', dataIndex: 'completionTokens' },
        { title: '费用估算', dataIndex: 'cost', render: (v: number) => `$${v.toFixed(4)}` },
      ]}
    />
  )
}

const SettingsTab: React.FC = () => {
  const [form] = Form.useForm()
  const [loading, setLoading] = React.useState(true)
  const [hasSecret, setHasSecret] = React.useState(false)
  React.useEffect(() => {
    adminSettings()
      .then((r) => {
        form.setFieldsValue({
          registerMode: r.settings.registerMode,
          feishuEnabled: r.settings.feishuEnabled,
          feishuAppId: r.settings.feishuAppId,
          feishuBaseUrl: r.settings.feishuBaseUrl,
        })
        setHasSecret(r.settings.feishuHasSecret ?? false)
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [form])
  return (
    <Form
      form={form}
      layout="vertical"
      style={{ maxWidth: 520 }}
      onFinish={async (v) => {
        try {
          await adminUpdateSettings(v)
          message.success('已保存')
        } catch (e) {
          message.error((e as Error).message)
        }
      }}
    >
      <Form.Item name="registerMode" label="注册策略">
        <Select
          options={[
            { value: 'open', label: '开放注册' },
            { value: 'invite', label: '邀请码制' },
            { value: 'closed', label: '关闭注册' },
          ]}
        />
      </Form.Item>
      <Form.Item name="feishuEnabled" label="飞书登录" valuePropName="checked">
        <Switch />
      </Form.Item>
      <Form.Item name="feishuAppId" label="飞书 App ID">
        <Input placeholder="cli_xxxx" />
      </Form.Item>
      <Form.Item name="feishuAppSecret" label={`飞书 App Secret${hasSecret ? '（已配置，留空不修改）' : ''}`}>
        <Input.Password placeholder="仅保存时提交，不回显" />
      </Form.Item>
      <Form.Item name="feishuBaseUrl" label="飞书开放平台地址（默认官方，测试可覆盖）">
        <Input placeholder="https://open.feishu.cn" />
      </Form.Item>
      <Button type="primary" htmlType="submit" loading={loading}>保存</Button>
    </Form>
  )
}

const AdminPage: React.FC = () => (
  <div>
    <h2>管理</h2>
    <Tabs
      items={[
        { key: 'stats', label: '用量/花费（30 天）', children: <StatsTab /> },
        { key: 'users', label: '用户', children: <UsersTab /> },
        { key: 'pricing', label: '模型价目表', children: <PricingTab /> },
        { key: 'proxies', label: '公共代理池', children: <ProxiesTab /> },
        { key: 'templates', label: '预制模板', children: <TemplatesTab /> },
        { key: 'settings', label: '系统设置', children: <SettingsTab /> },
      ]}
    />
  </div>
)

export default AdminPage