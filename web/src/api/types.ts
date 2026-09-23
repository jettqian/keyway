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
  /** 新建时可选：从预制模板快速开始（后端校验模板并记录复制计数） */
  fromTemplateId?: number
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
  channelOrder?: number[] | null
  restricted?: boolean
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
  /** 推理强度：openai reasoning_effort 原值 / anthropic thinking:N；null = 未开启 */
  reasoningEffort?: string | null
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
  failed?: boolean
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
  byUser?: StatsGroup[]
  byChannel: StatsGroup[]
  byModel: StatsGroup[]
  byKey: StatsGroup[]
  recent?: LatestUsage[]
}

export interface LatestUsage {
  createdAt: number
  channelId: number
  channelName?: string
  lineUrl?: string
  via?: string
  model: string
  upstreamModel?: string
  reasoningEffort?: string
  statusCode: number
}

/** 渠道×模型链路状态（统计页「链路状态」/ 管理端全局视角） */
export interface LinkStat {
  channelId: number
  channelName?: string
  owner?: string // 管理端：渠道所有者
  model: string
  attempts: number // 上游尝试次数（失败切换的中间尝试计入）
  ok: number
  errorRate: number // 0~100
  avgMs: number
  lastAt: number // 最近一次尝试（Unix 秒）
  breaker?: {
    failCount: number
    cooldownUntil: number
    lastError: string
  }
}

export interface ModelPricing {
  model: string
  inputPerM: number
  cachedInputPerM: number | null
  cacheWritePerM: number | null
  outputPerM: number
}

/** 官方价目远程同步源（名称 + 链接） */
export interface PricingSource {
  name: string
  url: string
}

/** 价目表列表响应：条目 + 远程同步元信息 */
export interface PricingListResponse {
  pricing: ModelPricing[]
  syncedAt?: string
  sources?: PricingSource[]
}

export interface CatalogModel {
  id: number
  name: string
  pricingModel: string
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

/** 渠道×模型熔断状态（仅熔断中的条目；无条目 = 关闭） */
export interface BreakerInfo {
  channelId: number
  channelName: string
  model: string
  failCount: number
  openedAt: number
  /** 冷却到期时间（Unix 秒）；到期后下一个请求放行单次试探 */
  cooldownUntil: number
  lastError: string
  updatedAt: number
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

/** 用户配置导入结果（合并模式摘要） */
export interface ConfigImportResult {
  keysCreated: number
  keysReused: number
  keysMissing: number
  channelsCreated: number
  channelsReused: number
  channelsDrafted: number
  channelsSkipped: number
  tokensCreated: number
  tokensSkipped: number
  warnings: string[]
}
