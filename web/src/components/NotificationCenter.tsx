// 通用通知结构：右上角铃铛 + 红点。通知由前端数据源**派生**（当前唯一类型：
// 模型目录中「尚未添加到本人任何渠道」的模型），不落库；已读状态按用户存
// localStorage，打开面板即视为已读（红点消失），目录再新增模型时 id 变化、
// 红点重现。后续新增通知类型只需在 buildNotifications 里追加生成器。
import React from 'react'
import { Badge, Button, Empty, Popover } from 'antd'
import { BellOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import dayjs from 'dayjs'
import { listCatalogModels, listChannels } from '../api'
import type { CatalogModel, User } from '../api/types'
import { useI18n } from '../i18n'
import type { DictKey } from '../i18n/zh'

type Translate = (key: DictKey, params?: Record<string, string | number>) => string

export interface AppNotification {
  /** 稳定 id（已读判定依据）：内容变化即换 id，从而驱动红点重现 */
  id: string
  title: string
  description: string
  /** Unix 秒，展示用 */
  time?: number
  /** 点击「去处理」的跳转地址 */
  to?: string
}

// 通知数据重算事件：页面内操作（如模型绑定保存）后触发，红点即时消隐
const REFRESH_EVENT = 'keyway-notify-refresh'
export const emitNotificationsRefresh = () => window.dispatchEvent(new Event(REFRESH_EVENT))

interface NotificationsContextValue {
  notifications: AppNotification[]
  unreadCount: number
  markAllRead: () => void
}

const NotificationsContext = React.createContext<NotificationsContextValue>({
  notifications: [],
  unreadCount: 0,
  markAllRead: () => {},
})

const readKey = (userId: number) => `kw-notify-read-${userId}`

function loadReadIds(userId: number): Set<string> {
  try {
    const raw = localStorage.getItem(readKey(userId))
    if (!raw) return new Set()
    const arr: unknown = JSON.parse(raw)
    return Array.isArray(arr) ? new Set(arr.filter((v): v is string => typeof v === 'string')) : new Set()
  } catch {
    return new Set()
  }
}

// 通知生成器：目录启用模型 ∖ 本人全部渠道的模型并集 = 可添加模型。
// id 取排序后的模型名集合（增删模型都会换 id）；时间取相关模型的最近更新时间。
async function buildNotifications(t: Translate): Promise<AppNotification[]> {
  const [channelsRes, catalogRes] = await Promise.all([listChannels(), listCatalogModels()])
  const used = new Set(channelsRes.channels.flatMap((c) => (Array.isArray(c.models) ? c.models : []).map((m) => m.trim())))
  const pending = catalogRes.models.filter((m: CatalogModel) => !used.has(m.name))
  if (pending.length === 0) return []
  const names = pending.map((m) => m.name).sort()
  return [
    {
      id: `new-models:${names.join(',')}`,
      title: t('notify.newModelsTitle'),
      description: t('notify.newModelsDesc', { models: names.join('、') }),
      time: Math.max(...pending.map((m) => m.updatedAt || 0)),
      to: '/models?tab=catalog',
    },
  ]
}

export const NotificationsProvider: React.FC<{ user: User | null; children: React.ReactNode }> = ({ user, children }) => {
  const { t } = useI18n()
  const [notifications, setNotifications] = React.useState<AppNotification[]>([])
  const [readIds, setReadIds] = React.useState<Set<string>>(new Set())

  const userId = user?.id ?? null

  React.useEffect(() => {
    if (userId == null) {
      setNotifications([])
      setReadIds(new Set())
      return
    }
    let alive = true
    setReadIds(loadReadIds(userId))
    const load = () => {
      buildNotifications(t)
        .then((list) => {
          if (!alive) return
          setNotifications(list)
          // 修剪已读集合：只保留仍然存在的通知，避免 localStorage 无限增长
          setReadIds((prev) => {
            const ids = new Set(list.map((n) => n.id))
            const next = new Set<string>()
            for (const id of prev) if (ids.has(id)) next.add(id)
            return next
          })
        })
        .catch(() => {})
    }
    load()
    window.addEventListener(REFRESH_EVENT, load)
    return () => {
      alive = false
      window.removeEventListener(REFRESH_EVENT, load)
    }
  }, [userId, t])

  const markAllRead = React.useCallback(() => {
    if (userId == null) return
    const next = new Set(readIds)
    for (const n of notifications) next.add(n.id)
    setReadIds(next)
    try {
      localStorage.setItem(readKey(userId), JSON.stringify([...next]))
    } catch {
      // 存储异常不影响使用，仅红点可能在下次登录重现
    }
  }, [userId, readIds, notifications])

  const unreadCount = React.useMemo(() => notifications.filter((n) => !readIds.has(n.id)).length, [notifications, readIds])

  return (
    <NotificationsContext.Provider value={{ notifications, unreadCount, markAllRead }}>
      {children}
    </NotificationsContext.Provider>
  )
}

export const NotificationBell: React.FC = () => {
  const { t } = useI18n()
  const nav = useNavigate()
  const { notifications, unreadCount, markAllRead } = React.useContext(NotificationsContext)
  const [open, setOpen] = React.useState(false)

  // 打开面板即视为已读（「看过之后红点消失」）。用未读数做守卫避免 markAllRead
  // → 状态更新 → 重触发的循环；同时覆盖「面板开着时新通知到达」的竞态
  React.useEffect(() => {
    if (open && unreadCount > 0) markAllRead()
  }, [open, unreadCount, markAllRead])

  const content = notifications.length === 0 ? (
    <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('notify.empty')} style={{ padding: '12px 0' }} />
  ) : (
    <div className="notify-list">
      {notifications.map((n) => (
        <div key={n.id} className="notify-item">
          <div className="notify-item-head">
            <span className="notify-item-title">{n.title}</span>
            {n.time ? <span className="notify-item-time">{dayjs(n.time * 1000).format('YYYY-MM-DD HH:mm')}</span> : null}
          </div>
          <div className="notify-item-desc">{n.description}</div>
          {n.to ? (
            <Button
              size="small"
              type="primary"
              onClick={() => {
                setOpen(false)
                nav(n.to!)
              }}
            >
              {t('notify.goAdd')}
            </Button>
          ) : null}
        </div>
      ))}
    </div>
  )

  return (
    <Popover
      placement="bottomRight"
      trigger="click"
      open={open}
      onOpenChange={setOpen}
      title={t('notify.title')}
      content={content}
      styles={{ body: { width: 340, maxWidth: 'calc(100vw - 32px)' } }}
    >
      <Badge dot={unreadCount > 0} offset={[-3, 3]}>
        <Button type="text" className="pref-trigger" aria-label={t('notify.title')} icon={<BellOutlined />} />
      </Badge>
    </Popover>
  )
}
