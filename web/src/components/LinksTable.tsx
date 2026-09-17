import React from 'react'
import { Table, Tag, Tooltip } from 'antd'
import dayjs from 'dayjs'
import type { LinkStat } from '../api/types'
import { fmtInt, fmtMs, formatDateTime } from '../format'
import { useI18n } from '../i18n'

// 评级最低样本量：低于此值不参与质量分级与"最优"标记（避免 1~2 次请求的偶然结果误导）
const MIN_SAMPLES = 5

// 渠道×模型链路状态表（统计页「链路状态」与管理端全局视角共用）。
// 按**模型分组**做同模型横向对比：组内渠道按质量排序（未熔断在前 → 错误率升序 →
// 延迟升序）、熔断沉底；质量分级颜色编码（优/良/差/熔断/样本不足），样本充足且
// 排名第一的组合标注「最优」；延迟相对模型内最优着色（绿 ≤1.5×，橙 ≤3×，红 >3×）
const LinksTable: React.FC<{ links: LinkStat[]; showOwner?: boolean }> = ({ links, showOwner }) => {
  const { t } = useI18n()

  const grade = (r: LinkStat, ref: number | null): { label: string; color: string } => {
    if (r.breaker) return { label: t('stats.gradeBroken'), color: 'volcano' }
    if (r.attempts < MIN_SAMPLES) return { label: t('stats.insufficient'), color: 'default' }
    const rel = ref && r.avgMs > 0 ? r.avgMs / ref : null
    if (r.errorRate >= 20) return { label: t('stats.gradePoor'), color: 'red' }
    if (r.errorRate >= 5) return rel != null && rel > 3 ? { label: t('stats.gradePoor'), color: 'red' } : { label: t('stats.gradeFair'), color: 'gold' }
    if (rel == null) return { label: t('stats.gradeGood'), color: 'green' }
    if (rel <= 1.5) return { label: t('stats.gradeGood'), color: 'green' }
    if (rel <= 3) return { label: t('stats.gradeFair'), color: 'gold' }
    return { label: t('stats.gradePoor'), color: 'red' }
  }

  // 分组与排序：模型按总尝试数降序；组内按质量排序并计算延迟基准与"最优"标记
  type Row = LinkStat & { rowSpan: number; best: boolean }
  const byModel = new Map<string, LinkStat[]>()
  for (const l of links) {
    byModel.set(l.model, [...(byModel.get(l.model) ?? []), l])
  }
  const rows: Row[] = []
  const refs = new Map<string, number | null>()
  const models = [...byModel.entries()].sort((a, b) => {
    const sum = (ls: LinkStat[]) => ls.reduce((n, l) => n + l.attempts, 0)
    return sum(b[1]) - sum(a[1])
  })
  for (const [model, ls] of models) {
    const sorted = [...ls].sort((a, b) => {
      if ((a.breaker ? 1 : 0) !== (b.breaker ? 1 : 0)) return (a.breaker ? 1 : 0) - (b.breaker ? 1 : 0)
      if (a.errorRate !== b.errorRate) return a.errorRate - b.errorRate
      return a.avgMs - b.avgMs
    })
    // 延迟基准：样本充足且错误率 ≤10% 的组合中的最小平均延迟（全组不合格时退化为
    // 样本充足组合的最小值；快速失败的渠道错误率高，不会成为基准）
    const qualified = sorted.filter((r) => !r.breaker && r.attempts >= MIN_SAMPLES)
    const good = qualified.filter((r) => r.errorRate <= 10)
    const pool = good.length ? good : qualified
    const ref = pool.length ? Math.min(...pool.map((r) => r.avgMs || Infinity)) : null
    refs.set(model, ref)
    const bestIdx = sorted.findIndex((r) => !r.breaker && r.attempts >= MIN_SAMPLES)
    sorted.forEach((r, i) => {
      rows.push({ ...r, rowSpan: i === 0 ? sorted.length : 0, best: i === bestIdx && sorted.length > 1 })
    })
  }

  const latencyRender = (r: LinkStat) => {
    if (r.attempts <= 0) return <span className="text-tertiary">—</span>
    const ref = refs.get(r.model)
    if (!ref || r.attempts < MIN_SAMPLES || r.errorRate >= 20) return fmtMs(r.avgMs)
    const rel = r.avgMs / ref
    const color = rel <= 1.5 ? '#389e0d' : rel <= 3 ? '#d46b08' : '#cf1322'
    return (
      <span style={{ color }}>
        {fmtMs(r.avgMs)}
        <span className="text-tertiary" style={{ fontSize: 12 }}> ×{rel.toFixed(1)}</span>
      </span>
    )
  }

  const columns = [
    {
      title: t('common.model'),
      dataIndex: 'model',
      width: 160,
      onCell: (r: Row) => ({ rowSpan: r.rowSpan }),
      render: (v: string) => <b>{v}</b>,
    },
    {
      title: t('stats.channel'),
      dataIndex: 'channelName',
      width: 170,
      ellipsis: true,
      render: (v: string, r: Row) => (
        <span>
          {v || `#${r.channelId}`}
          {r.best ? (
            <Tag color="gold" style={{ marginLeft: 6 }}>
              {t('stats.best')}
            </Tag>
          ) : null}
        </span>
      ),
    },
    ...(showOwner
      ? [
          {
            title: t('stats.owner'),
            dataIndex: 'owner' as const,
            width: 110,
            ellipsis: true,
            render: (v: string) => <span className="text-tertiary">{v || '—'}</span>,
          },
        ]
      : []),
    {
      title: t('stats.quality'),
      key: 'quality',
      width: 110,
      render: (_: unknown, r: Row) => {
        const g = grade(r, refs.get(r.model) ?? null)
        const tag = <Tag color={g.color}>{g.label}</Tag>
        if (!r.breaker) return tag
        return (
          <Tooltip
            title={
              <div style={{ maxWidth: 360 }}>
                <div>{t('stats.breakerFailCount', { count: r.breaker.failCount })}</div>
                <div>{t('stats.breakerNextTrial', { time: dayjs.unix(r.breaker.cooldownUntil).format('MM-DD HH:mm') })}</div>
                {r.breaker.lastError ? <div style={{ wordBreak: 'break-all' }}>{r.breaker.lastError}</div> : null}
              </div>
            }
          >
            {tag}
          </Tooltip>
        )
      },
    },
    {
      title: t('stats.requests'),
      dataIndex: 'attempts',
      align: 'right' as const,
      render: (v: number) => fmtInt(v),
    },
    {
      title: t('stats.errorRate'),
      dataIndex: 'errorRate',
      align: 'right' as const,
      render: (_: number, r: LinkStat) =>
        r.attempts <= 0 ? (
          <span className="text-tertiary">—</span>
        ) : r.errorRate <= 0 ? (
          <span style={{ color: '#389e0d' }}>0%</span>
        ) : (
          <Tag color={r.errorRate >= 50 ? 'red' : r.errorRate >= 20 ? 'volcano' : 'orange'}>{r.errorRate}%</Tag>
        ),
    },
    {
      title: t('stats.avgLatency'),
      dataIndex: 'avgMs',
      align: 'right' as const,
      render: (_: number, r: LinkStat) => latencyRender(r),
    },
    {
      title: t('stats.lastAt'),
      dataIndex: 'lastAt',
      width: 170,
      render: (v: number) => (v > 0 ? <span className="text-tertiary">{formatDateTime(v)}</span> : <span className="text-tertiary">—</span>),
    },
  ]

  return (
    <Table<Row>
      rowKey={(r) => `${r.channelId}-${r.model}`}
      size="small"
      pagination={false}
      scroll={{ x: 'max-content' }}
      dataSource={rows}
      columns={columns}
    />
  )
}

export default LinksTable
