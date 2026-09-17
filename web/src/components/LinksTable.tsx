import React from 'react'
import { Table, Tag, Tooltip } from 'antd'
import dayjs from 'dayjs'
import type { LinkStat } from '../api/types'
import { fmtInt, fmtMs, formatDateTime } from '../format'
import { useI18n } from '../i18n'

// 渠道×模型链路状态表（统计页「链路状态」与管理端全局视角共用）：
// 口径为上游尝试（失败切换的中间尝试计入），熔断列叠加当前熔断快照
const LinksTable: React.FC<{ links: LinkStat[]; showOwner?: boolean }> = ({ links, showOwner }) => {
  const { t } = useI18n()

  const ownerColumn = showOwner
    ? [
        {
          title: t('stats.owner'),
          dataIndex: 'owner' as const,
          width: 110,
          ellipsis: true,
          render: (v: string) => <span className="text-tertiary">{v || '—'}</span>,
        },
      ]
    : []

  const columns = [
    {
      title: t('stats.channel'),
      dataIndex: 'channelName' as const,
      width: 150,
      ellipsis: true,
      render: (v: string, r: LinkStat) => v || `#${r.channelId}`,
    },
    ...ownerColumn,
    {
      title: t('common.model'),
      dataIndex: 'model' as const,
      ellipsis: true,
      render: (v: string) => <b>{v}</b>,
    },
    {
      title: t('stats.requests'),
      dataIndex: 'attempts' as const,
      align: 'right' as const,
      render: (v: number) => fmtInt(v),
      sorter: (a: LinkStat, b: LinkStat) => a.attempts - b.attempts,
    },
    {
      title: t('stats.errorRate'),
      dataIndex: 'errorRate' as const,
      align: 'right' as const,
      render: (_: number, r: LinkStat) =>
        r.errorRate <= 0 ? <span className="text-tertiary">0%</span> : <Tag color={r.errorRate >= 50 ? 'red' : 'orange'}>{r.errorRate}%</Tag>,
      sorter: (a: LinkStat, b: LinkStat) => a.errorRate - b.errorRate,
    },
    {
      title: t('stats.avgLatency'),
      dataIndex: 'avgMs' as const,
      align: 'right' as const,
      render: (_: number, r: LinkStat) => (r.attempts > 0 ? fmtMs(r.avgMs) : <span className="text-tertiary">—</span>),
      sorter: (a: LinkStat, b: LinkStat) => a.avgMs - b.avgMs,
    },
    {
      title: t('stats.lastAt'),
      dataIndex: 'lastAt' as const,
      width: 170,
      render: (v: number) => (v > 0 ? <span className="text-tertiary">{formatDateTime(v)}</span> : <span className="text-tertiary">—</span>),
      sorter: (a: LinkStat, b: LinkStat) => a.lastAt - b.lastAt,
    },
    {
      title: t('stats.breaker'),
      dataIndex: 'breaker' as const,
      width: 90,
      render: (_: unknown, r: LinkStat) =>
        r.breaker ? (
          <Tooltip
            title={
              <div style={{ maxWidth: 360 }}>
                <div>{t('stats.breakerFailCount', { count: r.breaker.failCount })}</div>
                <div>{t('stats.breakerNextTrial', { time: dayjs.unix(r.breaker.cooldownUntil).format('MM-DD HH:mm') })}</div>
                {r.breaker.lastError ? <div style={{ wordBreak: 'break-all' }}>{r.breaker.lastError}</div> : null}
              </div>
            }
          >
            <Tag color="red" style={{ cursor: 'pointer' }}>
              {t('stats.breaker')}
            </Tag>
          </Tooltip>
        ) : (
          <span className="text-tertiary">—</span>
        ),
    },
  ]

  return (
    <Table<LinkStat>
      rowKey={(r) => `${r.channelId}-${r.model}`}
      size="small"
      pagination={{ pageSize: 20, hideOnSinglePage: true }}
      scroll={{ x: 'max-content' }}
      dataSource={links}
      columns={columns}
    />
  )
}

export default LinksTable
