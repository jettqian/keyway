import React from 'react'
import { Layout as AntLayout, Menu, Dropdown, message } from 'antd'
import {
  KeyOutlined,
  ApiOutlined,
  TagsOutlined,
  LockOutlined,
  FileTextOutlined,
  BarChartOutlined,
  SettingOutlined,
  UserOutlined,
  AppstoreOutlined,
  DeploymentUnitOutlined,
} from '@ant-design/icons'
import { Outlet, useNavigate, useLocation } from 'react-router-dom'
import { logout, me } from '../api'
import type { User } from '../api/types'

const { Sider, Header, Content } = AntLayout

export const ConsoleLayout: React.FC = () => {
  const nav = useNavigate()
  const location = useLocation()
  const [user, setUser] = React.useState<User | null>(null)

  React.useEffect(() => {
    me()
      .then((r) => setUser(r.user))
      .catch(() => nav('/login'))
  }, [nav])

  const isAdmin = user?.role === 100

  const items = [
    { key: '/keys', icon: <KeyOutlined />, label: '密钥池' },
    { key: '/channels', icon: <ApiOutlined />, label: '渠道' },
    { key: '/models', icon: <DeploymentUnitOutlined />, label: '渠道模型' },
    { key: '/templates', icon: <TagsOutlined />, label: '预制模板' },
    { key: '/tokens', icon: <LockOutlined />, label: '令牌' },
    { key: '/logs', icon: <FileTextOutlined />, label: '日志' },
    { key: '/stats', icon: <BarChartOutlined />, label: '统计' },
  ]
  if (isAdmin) {
    items.push({ key: '/admin', icon: <SettingOutlined />, label: '管理' })
  }

  return (
    <AntLayout style={{ minHeight: '100vh' }}>
      <Sider theme="light" breakpoint="lg" className="console-sider">
        <div className="brand">
          <div className="brand-mark">K</div>
          <div><div className="brand-name">Keyway</div><div className="brand-subtitle">模型网关控制台</div></div>
        </div>
        <Menu
          className="console-menu"
          mode="inline"
          selectedKeys={[items.find((item) => location.pathname === item.key || location.pathname.startsWith(`${item.key}/`))?.key ?? '/channels']}
          items={items}
          onClick={({ key }) => nav(key)}
        />
      </Sider>
      <AntLayout>
        <Header className="console-header" style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', paddingInline: 24 }}>
          <Dropdown
            menu={{
              items: [
                { key: 'logout', icon: <AppstoreOutlined />, label: '退出登录' },
              ],
              onClick: async ({ key }) => {
                if (key === 'logout') {
                  await logout()
                  message.success('已退出')
                  nav('/login')
                }
              },
            }}
          >
            <span style={{ cursor: 'pointer' }}>
              <UserOutlined /> {user?.username ?? '...'}
            </span>
          </Dropdown>
        </Header>
        <Content className="console-content">
          <Outlet context={{ user, setUser }} />
        </Content>
      </AntLayout>
    </AntLayout>
  )
}
