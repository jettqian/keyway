import React from 'react'
import { Tooltip } from 'antd'
import type { CatalogModel } from '../api/types'
import Money from './Money'
import { useI18n } from '../i18n'

/**
 * 模型目录关联单价展示（用户页与管理员页共用）：
 * 按模型名精确匹配 model_pricing，四档价直接显示生效数值——缓存档未配置时
 * 回退输入价（费用统计同口径）；无同名条目显示"未定价"。
 */
const CatalogPrice: React.FC<{ m: CatalogModel }> = ({ m }) => {
  const { t } = useI18n()
  if (m.inputPerM == null) {
    return (
      <Tooltip title={t('common.unpricedTip')}>
        <span className="text-tertiary">{t('common.unpriced')}</span>
      </Tooltip>
    )
  }
  return (
    <div className="catalog-price" style={{ lineHeight: 1.7 }}>
      <div className="text-tertiary" style={{ fontSize: 12, lineHeight: 1.5 }}>
        {t('models.pricingModel')}: {m.pricingModel || m.name}
      </div>
      <div>
        <span className="catalog-price-label">{t('common.input')}</span> <Money value={m.inputPerM} />
        <span className="catalog-price-sep">·</span>
        <span className="catalog-price-label">{t('common.output')}</span> <Money value={m.outputPerM} />
      </div>
      <div>
        <span className="catalog-price-label">{t('common.cacheRead')}</span> <Money value={m.cachedInputPerM ?? m.inputPerM} />
        <span className="catalog-price-sep">·</span>
        <span className="catalog-price-label">{t('common.cacheWrite')}</span> <Money value={m.cacheWritePerM ?? m.inputPerM} />
      </div>
    </div>
  )
}

export default CatalogPrice
