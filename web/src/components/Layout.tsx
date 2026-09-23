import React from 'react'
import { Layout as AntLayout, Menu, Dropdown, Avatar, Tag, message, Drawer, Button, Grid, Tooltip } from 'antd'
import {
  KeyOutlined,
  ApiOutlined,
  LockOutlined,
  FileTextOutlined,
  BarChartOutlined,
  BookOutlined,
  SettingOutlined,
  UserOutlined,
  DeploymentUnitOutlined,
  LogoutOutlined,
  MenuOutlined,
  SunOutlined,
  MoonOutlined,
  TranslationOutlined,
  GithubOutlined,
  SaveOutlined,
} from '@ant-design/icons'
import { Outlet, useNavigate, useLocation } from 'react-router-dom'
import { logout, me } from '../api'
import type { User } from '../api/types'
import { useI18n } from '../i18n'
import { useTheme } from '../theme'
import { GITHUB_URL } from '../constants'
import BackupModal from './BackupModal'
import { NotificationBell, NotificationsProvider } from './NotificationCenter'

const { Sider, Header, Content } = AntLayout

export const ConsoleLayout: React.FC = () => {
  const nav = useNavigate()
  const location = useLocation()
  const { t, locale, setLocale } = useI18n()
  const { mode, toggle } = useTheme()
  const [user, setUser] = React.useState<User | null>(null)
  // 窄屏（<lg）用头部汉堡 + 抽屉导航替代侧栏；首次渲染 screens 为空对象，
  // lg === false 判定让首帧按桌面渲染，避免移动端闪抽屉
  const screens = Grid.useBreakpoint()
  const isMobile = screens.lg === false
  const [navOpen, setNavOpen] = React.useState(false)
  const [backupOpen, setBackupOpen] = React.useState(false)

  React.useEffect(() => {
    me()
      .then((r) => setUser(r.user))
      .catch(() => nav('/login'))
  }, [nav])

  const isAdmin = user?.role === 100

  const items = [
    { key: '/keys', icon: <KeyOutlined />, label: t('layout.keys') },
    { key: '/channels', icon: <ApiOutlined />, label: t('layout.channels') },
    { key: '/models', icon: <DeploymentUnitOutlined />, label: t('layout.models') },
    { key: '/tokens', icon: <LockOutlined />, label: t('layout.tokens') },
    { key: '/guide', icon: <BookOutlined />, label: t('layout.guide') },
    { key: '/logs', icon: <FileTextOutlined />, label: t('layout.logs') },
    { key: '/stats', icon: <BarChartOutlined />, label: t('layout.stats') },
  ]
  if (isAdmin) {
    items.push({ key: '/admin', icon: <SettingOutlined />, label: t('layout.admin') })
  }

  const brand = (
    <div className="brand">
      <div className="brand-mark">K</div>
      <div><div className="brand-name">Keyway</div><div className="brand-subtitle">{t('layout.subtitle')}</div></div>
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
    <NotificationsProvider user={user}>
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
                  aria-label={t('layout.openNav')}
                  icon={<MenuOutlined />}
                  onClick={() => setNavOpen(true)}
                />
                <div className="brand-name nav-brand">Keyway</div>
                <div style={{ flex: 1 }} />
              </>
            )}
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              <Tooltip title={t('common.github')}>
                <Button
                  type="text"
                  className="pref-trigger"
                  aria-label={t('common.github')}
                  icon={<GithubOutlined />}
                  href={GITHUB_URL}
                  target="_blank"
                  rel="noreferrer"
                />
              </Tooltip>
              <Tooltip title={t('common.toggleTheme')}>
                <Button
                  type="text"
                  className="pref-trigger"
                  aria-label={t('common.toggleTheme')}
                  icon={mode === 'dark' ? <SunOutlined /> : <MoonOutlined />}
                  onClick={toggle}
                />
              </Tooltip>
              <Tooltip title={t('common.toggleLang')}>
                <Button
                  type="text"
                  className="pref-trigger"
                  aria-label={t('common.toggleLang')}
                  icon={<TranslationOutlined />}
                  onClick={() => setLocale(locale === 'zh' ? 'en' : 'zh')}
                >
                  {locale === 'zh' ? 'EN' : '中'}
                </Button>
              </Tooltip>
              <NotificationBell />
            </span>
            <Dropdown
              menu={{
                items: [
                  { key: 'backup', icon: <SaveOutlined />, label: t('layout.backup') },
                  { type: 'divider' },
                  { key: 'logout', icon: <LogoutOutlined />, label: t('layout.logout') },
                ],
                onClick: async ({ key }) => {
                  if (key === 'backup') {
                    setBackupOpen(true)
                    return
                  }
                  if (key === 'logout') {
                    await logout()
                    message.success(t('layout.loggedOut'))
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
                {isAdmin ? <Tag style={{ marginInlineEnd: 0 }}>{t('layout.adminTag')}</Tag> : null}
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
        <BackupModal open={backupOpen} onClose={() => setBackupOpen(false)} />
      </AntLayout>
    </NotificationsProvider>
  )
}
