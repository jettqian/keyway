import React from 'react'
import { Tooltip } from 'antd'

/**
 * 线路 URL 展示（统计页最近生效流量与日志页共用）：
 * URL 往往很长（自定义中转地址），整串显示挤占列宽；
 * 改为域名主体 + 路径弱化（同域名多线路仍可区分），悬停显示完整地址。
 * via（direct | personal | proxy:{id}）非空时弱色追加、并入悬停内容。
 */
const LineUrl: React.FC<{ url?: string | null; via?: string | null }> = ({ url, via }) => {
  if (!url) return <span className="text-tertiary">—</span>
  let host = url
  let path = ''
  try {
    const u = new URL(url)
    host = u.host
    path = u.pathname === '/' ? '' : u.pathname
  } catch {
    // 非 URL 格式（历史数据）原样展示
  }
  return (
    <Tooltip title={via ? `${url} · ${via}` : url}>
      <span>
        {host}
        {path ? <span className="text-tertiary">{path}</span> : null}
        {via ? <span className="text-tertiary"> · {via}</span> : null}
      </span>
    </Tooltip>
  )
}

export default LineUrl
