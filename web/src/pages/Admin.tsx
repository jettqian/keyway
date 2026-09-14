import React from 'react'
import { Tabs, Table, Form, Switch, Input, Button, message, Modal, Tag, Space, Typography } from 'antd'
import {
  adminUsers,
  adminSetUserStatus,
  adminResetPassword,
  adminPricing,
  adminProxies,
  adminTemplates,
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
  React.useEffect(() => {
    adminProxies()
      .then((r) => setProxies(r.proxies))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  return (
    <Table<Proxy>
      rowKey="id"
      loading={loading}
      dataSource={proxies}
      columns={[
        { title: '名称', dataIndex: 'name' },
        { title: '状态', dataIndex: 'enabled', render: (v: boolean) => (v ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>) },
        { title: '备注', dataIndex: 'note' },
      ]}
    />
  )
}

const TemplatesTab: React.FC = () => {
  const [templates, setTemplates] = React.useState<ChannelTemplate[]>([])
  const [loading, setLoading] = React.useState(true)
  React.useEffect(() => {
    adminTemplates()
      .then((r) => setTemplates(r.templates))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])
  return (
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
      ]}
    />
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
  React.useEffect(() => {
    adminSettings()
      .then((r) => form.setFieldsValue(r.settings))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [form])
  return (
    <Form
      form={form}
      layout="vertical"
      style={{ maxWidth: 480 }}
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
        <Input placeholder="open / invite / closed" />
      </Form.Item>
      <Form.Item name="feishuEnabled" label="飞书登录" valuePropName="checked">
        <Switch />
      </Form.Item>
      <Form.Item name="feishuAppId" label="飞书 App ID">
        <Input />
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