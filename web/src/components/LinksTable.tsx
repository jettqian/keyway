import React from 'react'
import { Table, Tag, Tooltip } from 'antd'
import dayjs from 'dayjs'
import type { LinkStat } from '../api/types'
import { fmtInt, fmtMs, formatDateTime } from '../format'
import { useI18n } from '../i18n'

// 评级最低样本量：低于此值不参与质量分级与"最优"标记（避免 1~2 次请求的偶然结果误导）
const MIN_SAMPLES = 5

type GroupHeader = {
  kind: 'header'
  key: string
  model: string
  channels: number
  attempts: number
  errorRate: number | null
}
type LinkRow = LinkStat & { kind: 'link'; key: string; best: boolean }
type Row = GroupHeader | LinkRow

// 渠道×模型链路状态表（统计页「链路状态」与管理端全局视角共用）。
// 按**模型分组**做同模型横向对比，每组一个带背景的分组头行（模型名 + 渠道数/
// 尝试数/组内错误率）；组内渠道按 错误率升序 → 延迟升序 排序、熔断沉底。
// **质量分级只看错误率**（可靠性）：优 <5%、良 <20%、差 ≥20%，样本 <5 次不评级，
// 熔断单独分级——0% 错误率的渠道不会因延迟高而被降到 1% 错误率的渠道之下；
// 速度对比放在延迟列：显示相对模型内最优的倍数并着色（绿 ≤1.5×，橙 ≤3×，红 >3×）
const LinksTable: React.FC<{ links: LinkStat[]; showOwner?: boolean }> = ({ links, showOwner }) => {
  const { t } = useI18n()

  const grade = (r: LinkStat): { label: string; color: string } => {
    if (r.breaker) return { label: t('stats.gradeBroken'), color: 'volcano' }
    if (r.attempts < MIN_SAMPLES) return { label: t('stats.insufficient'), color: 'default' }
    if (r.errorRate < 5) return { label: t('stats.gradeGood'), color: 'green' }
    if (r.errorRate < 20) return { label: t('stats.gradeFair'), color: 'gold' }
    return { label: t('stats.gradePoor'), color: 'red' }
  }

  // 分组与排序：模型按总尝试数降序；组内按质量排序并计算延迟基准与"最优"标记
  const byModel = new Map<string, LinkStat[]>()
  for (const l of links) {
    byModel.set(l.model, [...(byModel.get(l.model) ?? []), l])
  }
  const refs = new Map<string, number | null>()
  const rows: Row[] = []
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
    refs.set(model, pool.length ? Math.min(...pool.map((r) => r.avgMs || Infinity)) : null)
    const attempts = sorted.reduce((n, r) => n + r.attempts, 0)
    const ok = sorted.reduce((n, r) => n + r.ok, 0)
    rows.push({
      kind: 'header',
      key: `h-${model}`,
      model,
      channels: sorted.length,
      attempts,
      errorRate: attempts > 0 ? ((attempts - ok) * 100) / attempts : null,
    })
    const bestIdx = sorted.findIndex((r) => !r.breaker && r.attempts >= MIN_SAMPLES)
    sorted.forEach((r, i) => {
      rows.push({ ...r, kind: 'link', key: `${r.channelId}-${r.model}`, best: i === bestIdx && sorted.length > 1 })
    })
  }

  const latencyRender = (r: LinkRow) => {
    if (r.attempts <= 0) return <span className="text-tertiary">—</span>
    const ref = refs.get(r.model)
    if (!ref || r.attempts < MIN_SAMPLES) return fmtMs(r.avgMs)
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
      title: t('stats.channel'),
      key: 'channel',
      width: 190,
      ellipsis: true,
      render: (_: unknown, r: Row) =>
        r.kind === 'header' ? (
          <span>
            <b>{r.model}</b>
            <span className="text-tertiary" style={{ marginLeft: 10, fontSize: 12, fontWeight: 400 }}>
              {r.attempts > 0
                ? t('stats.groupMeta', { channels: r.channels, attempts: fmtInt(r.attempts), rate: (r.errorRate ?? 0).toFixed(1) })
                : t('stats.groupMetaNoRate', { channels: r.channels })}
            </span>
          </span>
        ) : (
          <span>
            {r.channelName || `#${r.channelId}`}
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
            key: 'owner',
            width: 110,
            ellipsis: true,
            render: (_: unknown, r: Row) =>
              r.kind === 'link' ? <span className="text-tertiary">{r.owner || '—'}</span> : null,
          },
        ]
      : []),
    {
      title: t('stats.quality'),
      key: 'quality',
      width: 110,
      render: (_: unknown, r: Row) => {
        if (r.kind !== 'link') return null
        const g = grade(r)
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
      key: 'attempts',
      align: 'right' as const,
      render: (_: unknown, r: Row) => (r.kind === 'link' ? fmtInt(r.attempts) : null),
    },
    {
      title: t('stats.errorRate'),
      key: 'errorRate',
      align: 'right' as const,
      render: (_: unknown, r: Row) => {
        if (r.kind !== 'link') return null
        if (r.attempts <= 0) return <span className="text-tertiary">—</span>
        if (r.errorRate <= 0) return <span style={{ color: '#389e0d' }}>0%</span>
        return <Tag color={r.errorRate >= 50 ? 'red' : r.errorRate >= 20 ? 'volcano' : 'orange'}>{r.errorRate}%</Tag>
      },
    },
    {
      title: t('stats.avgLatency'),
      key: 'avgMs',
      align: 'right' as const,
      render: (_: unknown, r: Row) => (r.kind === 'link' ? latencyRender(r) : null),
    },
    {
      title: t('stats.lastAt'),
      key: 'lastAt',
      width: 170,
      render: (_: unknown, r: Row) =>
        r.kind === 'link' && r.lastAt > 0 ? <span className="text-tertiary">{formatDateTime(r.lastAt)}</span> : null,
    },
  ]
  // 分组头行：首列横跨整行，其余列 colSpan=0 隐藏
  const total = columns.length
  columns.forEach((c, i) => {
    ;(c as { onCell?: unknown }).onCell = (r: Row) => (r.kind === 'header' ? { colSpan: i === 0 ? total : 0 } : {})
  })

  return (
    <Table<Row>
      rowKey={(r) => r.key}
      size="small"
      pagination={false}
      scroll={{ x: 'max-content' }}
      dataSource={rows}
      rowClassName={(r) => (r.kind === 'header' ? 'links-group-header' : '')}
      columns={columns}
    />
  )
}

export default LinksTable
