// 将后端 Unix 秒/毫秒时间戳或 ISO 字符串统一显示为 年/月/日 时:分:秒。
export function formatDateTime(value: number | string | null | undefined): string {
  if (value === null || value === undefined || value === '') return '—'
  const raw = typeof value === 'number' || /^\d+$/.test(value) ? Number(value) : NaN
  const date = Number.isFinite(raw) ? new Date(raw < 1e12 ? raw * 1000 : raw) : new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}/${pad(date.getMonth() + 1)}/${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

// 价格数字格式化：最多 4 位有效数字并去尾零（3 → 3、0.27 → 0.27、1.1 → 1.1），
// 避免浮点长尾与无效小数位
export function fmtPrice(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—'
  if (value === 0) return '0'
  const s = String(parseFloat(value.toPrecision(4)))
  if (s.includes('e')) return value.toFixed(8).replace(/0+$/, '').replace(/\.$/, '')
  return s
}

// 整数千分位（tokens/请求数等大数逐位可比）：1234567 → 1,234,567
export function fmtInt(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—'
  return new Intl.NumberFormat('en-US').format(value)
}

// 耗时人性化：832ms、1.24s、2m05s（超过 1 分钟才进位到分）
export function fmtMs(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value <= 0) return '-'
  if (value < 1000) return `${Math.round(value)}ms`
  if (value < 60_000) return `${(value / 1000).toFixed(2)}s`
  const m = Math.floor(value / 60_000)
  const s = Math.round((value % 60_000) / 1000)
  return `${m}m${String(s).padStart(2, '0')}s`
}

// 费用估算：固定 4 位小数便于列内对比；非零但低于显示下限时用 <$0.0001，
// 避免单条极小费用被 toFixed(4) 抹成 $0.0000 误读为分文未花
export function fmtCost(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—'
  if (value > 0 && value < 0.0001) return '<$0.0001'
  return `$${value.toFixed(4)}`
}
