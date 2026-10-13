import React from 'react'
import { Segmented, Tooltip } from 'antd'
import { exchangeRate } from './api'
import type { ExchangeRateInfo } from './api/types'
import { formatDateTime } from './format'
import { useI18n } from './i18n'

// 费用估算展示货币：cny（默认，按当前生效汇率折算）/ usd。
// 仅展示层折算——存储、统计接口与导出 CSV 恒为 USD；价目单价（模型属性）不随切换
export type Currency = 'usd' | 'cny'

const STORAGE_KEY = 'kw-currency'

// 汇率模块级缓存：汇率至多 24h 同步一次，会话内拉取一次即可；拉取失败回退 USD 展示
let rateCache: ExchangeRateInfo | null = null
let rateFetching: Promise<ExchangeRateInfo | null> | null = null

function fetchRate(): Promise<ExchangeRateInfo | null> {
  if (rateCache) return Promise.resolve(rateCache)
  rateFetching ??= exchangeRate()
    .then((r) => {
      if (r.rate > 0) rateCache = r
      return rateCache
    })
    .catch(() => null)
  return rateFetching
}

interface CurrencyContextValue {
  currency: Currency
  setCurrency: (c: Currency) => void
  rate: ExchangeRateInfo | null
}

const CurrencyContext = React.createContext<CurrencyContextValue>({
  currency: 'cny',
  setCurrency: () => {},
  rate: null,
})

export const CurrencyProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  // 默认 CNY；仅显式选过 USD 的浏览器保持 USD（旧版存过 'cny' 或无记录均归 CNY）
  const [currency, setCurrencyState] = React.useState<Currency>(() =>
    localStorage.getItem(STORAGE_KEY) === 'usd' ? 'usd' : 'cny',
  )
  const [rate, setRate] = React.useState<ExchangeRateInfo | null>(rateCache)

  React.useEffect(() => {
    let mounted = true
    fetchRate().then((r) => {
      if (mounted && r) setRate(r)
    })
    return () => {
      mounted = false
    }
  }, [])

  const setCurrency = React.useCallback((c: Currency) => {
    setCurrencyState(c)
    localStorage.setItem(STORAGE_KEY, c)
  }, [])

  const value = React.useMemo(() => ({ currency, setCurrency, rate }), [currency, setCurrency, rate])
  return <CurrencyContext.Provider value={value}>{children}</CurrencyContext.Provider>
}

// useCurrency 货币上下文；Money 组件 cost 口径据此折算
export const useCurrency = () => React.useContext(CurrencyContext)

// CurrencyToggle 费用货币切换（统计页与管理员看板工具条共用）；CNY 下提示折算口径
export const CurrencyToggle: React.FC = () => {
  const { t } = useI18n()
  const { currency, setCurrency, rate } = useCurrency()
  const tip =
    currency === 'cny' && rate
      ? t('stats.currencyCnyTip', {
          rate: rate.rate,
          source: rate.mode === 'manual' ? t('stats.currencyManual') : rate.source ?? '—',
          time: rate.updatedAt ? formatDateTime(rate.updatedAt) : '—',
        })
      : t('stats.currencyTip')
  return (
    <Tooltip title={tip}>
      <Segmented
        size="small"
        value={currency}
        onChange={(v) => setCurrency(v as Currency)}
        options={[
          { label: 'USD', value: 'usd' },
          { label: 'CNY', value: 'cny' },
        ]}
      />
    </Tooltip>
  )
}
