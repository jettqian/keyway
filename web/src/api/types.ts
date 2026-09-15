export interface User {
  id: number
  username: string
  role: number
  status: number
  hasFeishu: boolean
  createdAt: string
  lastLoginAt: string
}

export interface ApiKey {
  id: number
  name: string
  note: string
  status: number
  lastError: string
  cooldownUntil: number
  createdAt: string
}

export interface ApiKeyInput {
  name: string
  note?: string
  value?: string
}

export interface Channel {
  id: number
  name: string
  type: '' | 'openai' | 'anthropic'
  baseUrls: string[]
  keyIds: number[]
  keyStrategy: 'ordered' | 'round_robin'
  lineStrategy: 'auto' | 'manual'
  proxyUrl?: string
  allowPublicProxy: boolean
  models: string[]
  modelMapping: Record<string, string>
  forwardMode: 'passthrough' | 'convert'
  priority: number
  priceMultiplier?: number
  pricingMode?: 'usd' | 'cny_ratio'
  cnyRatio?: number
  isDefault: boolean
  enabled: boolean
  copiedFromTemplateId?: number
  lastError?: string
  createdAt: string
}

export interface ChannelInput {
  name: string
  type: '' | 'openai' | 'anthropic'
  baseUrls: string[]
  keyIds: number[]
  keyStrategy: 'ordered' | 'round_robin'
  lineStrategy: 'auto' | 'manual'
  proxyUrl?: string
  allowPublicProxy: boolean
  models: string[]
  modelMapping: Record<string, string>
  forwardMode: 'passthrough' | 'convert'
  priority: number
  priceMultiplier?: number
  pricingMode?: 'usd' | 'cny_ratio'
  cnyRatio?: number
  isDefault: boolean
  enabled: boolean
}

export interface ChannelTemplate {
  id: number
  name: string
  type: '' | 'openai' | 'anthropic'
  baseUrls: string[]
  lineStrategy: 'auto' | 'manual'
  models: string[]
  modelMapping: Record<string, string>
  priorityDefault: number
  allowPublicProxyDefault: boolean
  note: string
  enabled: boolean
  copyCount: number
  updatedAt: string
}

export interface GatewayToken {
  id: number
  name: string
  keyPrefix: string
  channelId?: number
  channelIds?: number[]
  modelScope?: string
  expiresAt?: string
  revoked: boolean
  createdAt: string
}

export interface GatewayTokenCreated {
  token: GatewayToken
  plaintext: string
}

export interface LogEntry {
  id: number
  createdAt: string
  channelId: number
  channelName: string
  lineUrl: string
  via: string
  keyId: number
  protocol: 'openai' | 'anthropic'
  model: string
  upstreamModel: string
  statusCode: number
  ttftMs: number
  totalMs: number
  promptTokens: number
  completionTokens: number
  cachedTokens: number
  cacheWriteTokens: number
  inputCost: number | null
  outputCost: number | null
  error: string
}

export interface LogQuery {
  page: number
  pageSize: number
  start?: string
  end?: string
  channelId?: number
  model?: string
  statusCode?: number
}

export interface StatsSummary {
  requests: number
  errorRate: number
  promptTokens: number
  completionTokens: number
  cost: number
  unpriced: boolean
}

export interface StatsGroup {
  dim: string
  requests: number
  promptTokens: number
  completionTokens: number
  cost: number
  errors: number
}

export interface StatsResponse {
  summary: StatsSummary
  byChannel: StatsGroup[]
  byModel: StatsGroup[]
  byKey: StatsGroup[]
  latest?: LatestUsage | null
}

export interface LatestUsage {
  createdAt: number
  channelId: number
  channelName?: string
  model: string
  upstreamModel?: string
  statusCode: number
}

export interface ModelPricing {
  model: string
  inputPerM: number
  cachedInputPerM: number | null
  cacheWritePerM: number | null
  outputPerM: number
}

export interface CatalogModel {
  id: number
  name: string
  note: string
  enabled: boolean
  updatedAt: number
  inputPerM?: number
  outputPerM?: number
  cachedInputPerM?: number | null
  cacheWritePerM?: number | null
}

export interface Proxy {
  id: number
  name: string
  enabled: boolean
  note: string
}

export interface AdminSettings {
  registerMode: 'open' | 'invite' | 'closed'
  feishuEnabled: boolean
  feishuAppId: string
  feishuAppSecret?: string
  feishuHasSecret?: boolean
  feishuBaseUrl?: string
  exchangeRate?: number
  /** auto = 定时同步；manual = 固定值 */
  exchangeRateMode?: 'auto' | 'manual'
  exchangeRateSource?: string
  /** 命中汇率源的请求地址 */
  exchangeRateSourceUrl?: string
  exchangeRateUpdatedAt?: string
}

/** 汇率手动同步结果（apply=false 时仅预览） */
export interface ExchangeRateSyncResult {
  rate: number
  source: string
  sourceUrl?: string
  applied: boolean
  updatedAt?: string
}

export interface PublicInfo {
  feishuEnabled: boolean
  registerMode: 'open' | 'invite' | 'closed'
}

export interface ProxyInput {
  name: string
  url: string
  note?: string
  enabled?: boolean
}

export interface TemplateInput {
  name: string
  type?: '' | 'openai' | 'anthropic'
  baseUrls: string[]
  lineStrategy: 'auto' | 'manual'
  models: string[]
  modelMapping: Record<string, string>
  priorityDefault: number
  allowPublicProxyDefault: boolean
  note?: string
  enabled: boolean
}
