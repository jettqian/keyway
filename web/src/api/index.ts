import { get, post, put, del } from './client'
import type {
  User,
  ApiKey,
  ApiKeyInput,
  Channel,
  ChannelInput,
  GatewayToken,
  GatewayTokenCreated,
  ChannelTemplate,
  TemplateInput,
  LogEntry,
  LogQuery,
  StatsResponse,
  LinkStat,
  ModelPricing,
  PricingListResponse,
  CatalogModel,
  Proxy,
  ProxyInput,
  AdminSettings,
  ExchangeRateSyncResult,
  PublicInfo,
  BreakerInfo,
  ConfigImportResult,
} from './types'

// ---------- 认证 ----------
export const login = (username: string, password: string) =>
  post<{ user: User }>('/api/auth/login', { username, password })
export const register = (username: string, password: string, inviteCode?: string) =>
  post<{ user: User }>('/api/auth/register', { username, password, inviteCode })
export const logout = () => post<void>('/api/auth/logout')
export const me = () => get<{ user: User }>('/api/auth/me')
export const changePassword = (oldPassword: string, newPassword: string) =>
  put<void>('/api/auth/password', { oldPassword, newPassword })
export const publicInfo = () => get<PublicInfo>('/api/auth/public-info')
export const feishuLoginUrl = () => get<{ url: string }>('/api/auth/feishu/url')

// ---------- 密钥池 ----------
export const listKeys = () => get<{ keys: ApiKey[] }>('/api/keys')
export const createKey = (input: ApiKeyInput) => post<{ key: ApiKey }>('/api/keys', input)
export const updateKey = (id: number, input: ApiKeyInput) => put<{ key: ApiKey }>(`/api/keys/${id}`, input)
export const updateKeyStatus = (id: number, status: 1 | 2) =>
  put<void>(`/api/keys/${id}/status`, { status })
export const deleteKey = (id: number) => del<void>(`/api/keys/${id}`)

// ---------- 渠道 ----------
export const listChannels = () => get<{ channels: Channel[] }>('/api/channels')
export const createChannel = (input: ChannelInput) => post<{ channel: Channel }>('/api/channels', input)
export const updateChannel = (id: number, input: ChannelInput) =>
  put<{ channel: Channel }>(`/api/channels/${id}`, input)
export const deleteChannel = (id: number) => del<void>(`/api/channels/${id}`)
export const testChannel = (id: number) => post<{ results: ChannelTestResult[] }>(`/api/channels/${id}/test`)

export interface ChannelTestResult {
  lineUrl: string
  via: string
  ok: boolean
  latencyMs: number
  error: string
}

// ---------- 模板 ----------
export const listTemplates = () => get<{ templates: ChannelTemplate[] }>('/api/templates')

// ---------- 渠道×模型熔断 ----------
export const listBreakers = () => get<{ breakers: BreakerInfo[] }>('/api/breakers')
// 手动恢复：model 省略 = 恢复该渠道全部模型；下一个请求即恢复路由
export const resetBreaker = (channelId: number, model?: string) =>
  post<void>('/api/breakers/reset', { channelId, model: model || undefined })

// ---------- 令牌 ----------
export const listTokens = () => get<{ tokens: GatewayToken[] }>('/api/tokens')
export const createToken = (input: { name: string; channelIds?: number[]; modelScope?: string; expiresAt?: string }) =>
  post<GatewayTokenCreated>('/api/tokens', input)
export const updateToken = (id: number, input: { name?: string; channelIds?: number[]; channelOrder?: number[]; restricted?: boolean }) =>
  put<{ token: GatewayToken }>(`/api/tokens/${id}`, input)
export const revokeToken = (id: number) => post<void>(`/api/tokens/${id}/revoke`)
export const deleteToken = (id: number) => del<void>(`/api/tokens/${id}`)
export const revealToken = (id: number) => post<{ plaintext: string }>(`/api/tokens/${id}/reveal`)

// ---------- 日志与统计 ----------
export const listLogs = (q: LogQuery) =>
  get<{ logs: LogEntry[]; total: number }>(`/api/logs?${qs(q as unknown as Record<string, unknown>)}`)

// 统计时间窗：start/end（YYYY-MM-DD，end 含当天）自定义起止；缺省回退 days；
// tokenId 可选，仅统计该令牌产生的请求
export interface StatsRange {
  days?: number
  start?: string
  end?: string
  tokenId?: number
}
export const myStats = (q: StatsRange) =>
  get<StatsResponse>(`/api/stats?${qs(q as unknown as Record<string, unknown>)}`)
// 渠道×模型链路状态（本人渠道；口径同统计——每次上游尝试各计一次）
export const myStatsLinks = (q: StatsRange) =>
  get<{ links: LinkStat[] }>(`/api/stats/links?${qs(q as unknown as Record<string, unknown>)}`)

// ---------- 管理员 ----------
export const adminUsers = () => get<{ users: User[] }>('/api/admin/users')
export const adminSetUserStatus = (id: number, status: number) =>
  put<{ user: User }>(`/api/admin/users/${id}/status`, { status })
export const adminResetPassword = (id: number) => post<{ password: string }>(`/api/admin/users/${id}/reset_password`)
export const adminStats = (q: StatsRange) =>
  get<StatsResponse>(`/api/admin/stats?${qs(q as unknown as Record<string, unknown>)}`)
// 管理端全局链路状态：聚合所有用户的请求，行含渠道所有者
export const adminStatsLinks = (q: StatsRange) =>
  get<{ links: LinkStat[] }>(`/api/admin/stats/links?${qs(q as unknown as Record<string, unknown>)}`)
export const adminPricing = () => get<PricingListResponse>('/api/admin/pricing')
export const adminUpdatePricing = (p: ModelPricing) =>
  put<{ pricing: ModelPricing }>(`/api/admin/pricing/${encodeURIComponent(p.model)}`, p)
export const adminDeletePricing = (model: string) =>
  del<void>(`/api/admin/pricing/${encodeURIComponent(model)}`)
export const adminSyncRemotePricing = () =>
  post<{ result: { refreshed: number; missing: number; warnings?: string[] } }>(
    '/api/admin/pricing/sync_remote',
  )
export const adminProxies = () => get<{ proxies: Proxy[] }>('/api/admin/proxies')
export const adminCreateProxy = (input: ProxyInput) => post<{ proxy: Proxy }>('/api/admin/proxies', input)
export const adminUpdateProxy = (id: number, input: Partial<ProxyInput>) =>
  put<void>(`/api/admin/proxies/${id}`, input)
export const adminDeleteProxy = (id: number) => del<void>(`/api/admin/proxies/${id}`)
export const adminProxyUsage = () =>
  get<{ usage: Array<{ userId: number; username: string; proxyId: number; proxyName: string; day: string; bytes: number }> }>(
    '/api/admin/proxy_usage',
  )
export const adminTemplates = () => get<{ templates: ChannelTemplate[] }>('/api/admin/templates')
export const adminCreateTemplate = (input: TemplateInput) =>
  post<{ template: ChannelTemplate }>('/api/admin/templates', input)
export const adminUpdateTemplate = (id: number, input: TemplateInput) =>
  put<{ template: ChannelTemplate }>(`/api/admin/templates/${id}`, input)
export const adminDeleteTemplate = (id: number) => del<void>(`/api/admin/templates/${id}`)
export const adminSettings = () => get<{ settings: AdminSettings }>('/api/admin/settings')
export const adminUpdateSettings = (s: AdminSettings) => put<{ settings: AdminSettings }>('/api/admin/settings', s)
export const adminSyncExchangeRate = (apply: boolean) =>
  post<{ result: ExchangeRateSyncResult }>('/api/admin/exchange-rate/sync', { apply })

function qs(q: Record<string, unknown>): string {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== null && v !== '') {
      params.set(k, String(v))
    }
  }
  return params.toString()
}

// ---------- 模型列表 ----------
export const updateModelBindings = (input: { name: string; previousName: string; channelIds: number[] }) =>
  put<void>('/api/models/bindings', input)

// ---------- 全局模型目录（管理员预置） ----------
export const listCatalogModels = () => get<{ models: CatalogModel[] }>('/api/models/catalog')
export const adminCatalogModels = () => get<{ models: CatalogModel[] }>('/api/admin/models')
export const adminCreateCatalogModel = (input: { name: string; pricingModel?: string; note?: string }) =>
  post<{ model: CatalogModel }>('/api/admin/models', input)
export const adminUpdateCatalogModel = (id: number, input: { name?: string; pricingModel?: string; note?: string; enabled?: boolean }) =>
  put<{ model: CatalogModel }>(`/api/admin/models/${id}`, input)
export const adminDeleteCatalogModel = (id: number) => del<void>(`/api/admin/models/${id}`)

// ---------- 用户配置备份 ----------
// 导出为文件下载（raw fetch + blob，见 BackupModal）；导入直接 POST 文件内容
export const importConfig = (file: unknown) =>
  post<{ result: ConfigImportResult }>('/api/config/import', file)
