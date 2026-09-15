import React from 'react'
import { fmtCostParts, fmtPrice } from '../format'

/**
 * 货币展示（用户页与管理员页共用）：$ 符号小一号、弱色，数字主体突出。
 * - mode="cost"：费用口径，固定 4 位小数（表格列内可比），big 模式下额外弱化小数拖尾
 * - mode="price"：价目口径，fmtPrice 自适应精度（3 → 3、0.27 → 0.27）
 */
const Money: React.FC<{
  value: number | null | undefined
  mode?: 'cost' | 'price'
  big?: boolean
}> = ({ value, mode = 'price', big = false }) => {
  if (mode === 'price') {
    if (value == null || !Number.isFinite(value)) return <span className="text-tertiary">—</span>
    return (
      <span className="money">
        <span className="money-sym">$</span>
        {fmtPrice(value)}
      </span>
    )
  }
  const p = fmtCostParts(value)
  if (!p) return <span className="text-tertiary">—</span>
  return (
    <span className={`money${big ? ' money-big' : ''}`}>
      <span className="money-sym">{p.sym}</span>
      {p.int}
      {p.dec ? <span className="money-dec">.{p.dec}</span> : null}
    </span>
  )
}

export default Money
