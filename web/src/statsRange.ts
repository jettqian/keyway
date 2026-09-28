// 统计页时间范围：持久化到 localStorage（key 同主题约定的 kw- 前缀），
// 刷新后还原上次选择；存储值为 YYYY-MM-DD，读取时校验合法性，非法回退默认近 7 天
import React from 'react'
import dayjs, { type Dayjs } from 'dayjs'

const STORAGE_KEY = 'kw-stats-range'

// 与 Stats 页初始值一致：近 7 天（含今天）
function defaultRange(): [Dayjs, Dayjs] {
  return [dayjs().subtract(6, 'day'), dayjs()]
}

function readSavedRange(): [Dayjs, Dayjs] {
  try {
    const raw = JSON.parse(localStorage.getItem(STORAGE_KEY) || '')
    if (typeof raw?.start !== 'string' || typeof raw?.end !== 'string') return defaultRange()
    const start = dayjs(raw.start)
    const end = dayjs(raw.end)
    if (!start.isValid() || !end.isValid() || start.isAfter(end)) return defaultRange()
    return [start, end]
  } catch {
    return defaultRange()
  }
}

export function useStatsRange(): [[Dayjs, Dayjs], (v: [Dayjs, Dayjs]) => void] {
  const [range, setRange] = React.useState<[Dayjs, Dayjs]>(readSavedRange)

  const update = React.useCallback((v: [Dayjs, Dayjs]) => {
    setRange(v)
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ start: v[0].format('YYYY-MM-DD'), end: v[1].format('YYYY-MM-DD') }))
  }, [])

  return [range, update]
}
