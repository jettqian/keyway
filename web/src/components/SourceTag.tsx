import React from 'react'
import { Tag } from 'antd'
import { useI18n } from '../i18n'

/**
 * 价目来源标签（价目表条目、目录单价列、关联价目候选项共用）：
 * 手工/导入灰标、远程源（LiteLLM / models.dev）紫标；空串（历史数据）不显示。
 */
const SourceTag: React.FC<{ source?: string }> = ({ source }) => {
  const { t } = useI18n()
  if (!source) return null
  if (source === 'manual') return <Tag>{t('admin.srcManual')}</Tag>
  if (source === 'import') return <Tag>{t('admin.srcImport')}</Tag>
  return <Tag color="purple">{source}</Tag>
}

export default SourceTag
