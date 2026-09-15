import React from 'react'
import { Tooltip } from 'antd'
import type { CatalogModel } from '../api/types'

/**
 * 模型目录关联单价展示（用户页与管理员页共用）：
 * 主行显示输入/输出价，副行显示缓存读/写价（未配置按输入价回退，费用统计同口径）；
 * 价目表中无同名条目时显示"未定价"。
 */
const CatalogPrice: React.FC<{ m: CatalogModel }> = ({ m }) => {
  if (m.inputPerM == null) {
    return (
      <Tooltip title="价目表中无同名条目，使用该模型的请求费用将记为未定价">
        <span style={{ color: '#999' }}>未定价</span>
      </Tooltip>
    )
  }
  const hasCache = m.cachedInputPerM != null || m.cacheWritePerM != null
  return (
    <div style={{ lineHeight: 1.6 }}>
      <div>
        输入 {m.inputPerM} / 输出 {m.outputPerM}
      </div>
      <div style={{ color: '#999', fontSize: 12 }}>
        {hasCache
          ? `缓存读 ${m.cachedInputPerM ?? '同输入'} / 写 ${m.cacheWritePerM ?? '同输入'}`
          : '缓存未配置，按输入价计'}
      </div>
    </div>
  )
}

export default CatalogPrice
