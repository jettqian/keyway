import React from 'react'
import { Popover, Tag } from 'antd'
import { useI18n } from '../i18n'

/**
 * 模型列表标签展示（渠道列表与预制模板列表共用）：
 * 模型数量可能很多，行内只直接展示前 max 个，其余收进「+N」标签的
 * 气泡（标题含总数，内容区滚动浏览全部），避免撑爆行高与列宽。
 */
const ModelTags: React.FC<{ models?: string[] | null; max?: number }> = ({ models, max = 5 }) => {
  const { t } = useI18n()
  const list = (models ?? []).filter(Boolean)
  if (list.length === 0) return <span className="text-tertiary">—</span>
  const shown = list.slice(0, max)
  const rest = list.length - shown.length
  return (
    <span style={{ display: 'inline-block', maxWidth: 420 }}>
      {shown.map((m) => (
        <Tag key={m}>{m}</Tag>
      ))}
      {rest > 0 ? (
        <Popover
          title={t('common.allModelsCount', { count: list.length })}
          content={
            <div style={{ maxWidth: 360, maxHeight: 280, overflowY: 'auto' }}>
              {list.map((m) => (
                <Tag key={m}>{m}</Tag>
              ))}
            </div>
          }
        >
          <Tag style={{ cursor: 'pointer' }}>+{rest}</Tag>
        </Popover>
      ) : null}
    </span>
  )
}

export default ModelTags
