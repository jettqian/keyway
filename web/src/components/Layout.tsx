import React from 'react'
import { Layout as AntLayout, Menu, Dropdown, Avatar, Tag, message, Drawer, Button, Grid } from 'antd'
import {
  KeyOutlined,
  ApiOutlined,
  TagsOutlined,
  LockOutlined,
  FileTextOutlined,
  BarChartOutlined,
  BookOutlined,
  SettingOutlined,
  UserOutlined,
  DeploymentUnitOutlined,
  LogoutOutlined,
  MenuOutlined,
} from '@ant-design/icons'
import { Outlet, useNavigate, useLocation } from 'react-router-dom'
import { logout, me } from '../api'
import type { User } from '../api/types'

const { Sider, Header, Content } = AntLayout

export const ConsoleLayout: React.FC = () => {
  const nav = useNavigate()
  const location = useLocation()
  const [user, setUser] = React.useState<User | null>(null)
  // 窄屏（<lg）用头部汉堡 + 抽屉导航替代侧栏；首次渲染 screens 为空对象，
  // lg === false 判定让首帧按桌面渲染，避免移动端闪抽屉
  const screens = Grid.useBreakpoint()
  const isMobile = screens.lg === false
  const [navOpen, setNavOpen] = React.useState(false)

  React.useEffect(() => {
    me()
      .then((r) => setUser(r.user))
      .catch(() => nav('/login'))
  }, [nav])

  const isAdmin = user?.role === 100

  const items = [
    { key: '/keys', icon: <KeyOutlined />, label: '密钥池' },
    { key: '/channels', icon: <ApiOutlined />, label: '渠道' },
    { key: '/models', icon: <DeploymentUnitOutlined />, label: '模型管理' },
    { key: '/templates', icon: <TagsOutlined />, label: '预制模板' },
    { key: '/tokens', icon: <LockOutlined />, label: '令牌' },
    { key: '/guide', icon: <BookOutlined />, label: '接入指南' },
    { key: '/logs', icon: <FileTextOutlined />, label: '日志' },
    { key: '/stats', icon: <BarChartOutlined />, label: '统计' },
  ]
  if (isAdmin) {
    items.push({ key: '/admin', icon: <SettingOutlined />, label: '管理' })
  }

  const brand = (
    <div className="brand">
      <div className="brand-mark">K</div>
      <div><div className="brand-name">Keyway</div><div className="brand-subtitle">模型网关控制台</div></div>
    </div>
  )

  const menuEl = (
    <Menu
      className="console-menu"
      mode="inline"
      selectedKeys={[items.find((item) => location.pathname === item.key || location.pathname.startsWith(`${item.key}/`))?.key ?? '/channels']}
      items={items}
      onClick={({ key }) => {
        nav(key)
        setNavOpen(false)
      }}
    />
  )

  return (
    <AntLayout style={{ minHeight: '100vh' }}>
      {!isMobile && (
        <Sider theme="light" className="console-sider">
          {brand}
          {menuEl}
        </Sider>
      )}
      <AntLayout>
        <Header className="console-header" style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', paddingInline: 24 }}>
          {isMobile && (
            <>
              <Button
                type="text"
                className="nav-trigger"
                aria-label="打开导航菜单"
                icon={<MenuOutlined />}
                onClick={() => setNavOpen(true)}
              />
              <div className="brand-name nav-brand">Keyway</div>
              <div style={{ flex: 1 }} />
            </>
          )}
          <Dropdown
            menu={{
              items: [
                { key: 'logout', icon: <LogoutOutlined />, label: '退出登录' },
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
            <span className="user-chip">
              <Avatar size={26} icon={<UserOutlined />}>
                {user?.username?.slice(0, 1).toUpperCase()}
              </Avatar>
              <span className="user-chip-name">{user?.username ?? '...'}</span>
              {isAdmin ? <Tag style={{ marginInlineEnd: 0 }}>管理员</Tag> : null}
            </span>
          </Dropdown>
        </Header>
        <Content className="console-content">
          <Outlet context={{ user, setUser }} />
        </Content>
      </AntLayout>
      <Drawer
        placement="left"
        width={280}
        open={navOpen && isMobile}
        onClose={() => setNavOpen(false)}
        className="nav-drawer"
        styles={{ body: { padding: 0 } }}
        closable={false}
      >
        {brand}
        {menuEl}
      </Drawer>
    </AntLayout>
  )
}
