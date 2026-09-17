import React from 'react'
import { Button, Card, Col, DatePicker, Empty, Row, Select, Space, Statistic, Table, Tag, Tooltip, message } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { listTokens, myStats, myStatsLinks } from '../api'
import type { GatewayToken, LatestUsage, LinkStat, StatsResponse, StatsGroup } from '../api/types'
import Money from '../components/Money'
import LineUrl from '../components/LineUrl'
import LinksTable from '../components/LinksTable'
import { formatDateTime, fmtInt, fmtTokens } from '../format'
import { useI18n } from '../i18n'
import type { DictKey } from '../i18n/zh'

// 快捷时间项 label：随语言切换渲染，供本页与 Admin 页共享
const LocaleText: React.FC<{ k: DictKey }> = ({ k }) => {
  const { t } = useI18n()
  return <>{t(k)}</>
}

// 快捷时间项（自然日口径）
export const rangePresets: { label: React.ReactNode; value: [Dayjs, Dayjs] }[] = [
  { label: <LocaleText k="stats.today" />, value: [dayjs(), dayjs()] },
  { label: <LocaleText k="stats.yesterday" />, value: [dayjs().subtract(1, 'day'), dayjs().subtract(1, 'day')] },
  { label: <LocaleText k="stats.last7Days" />, value: [dayjs().subtract(6, 'day'), dayjs()] },
  { label: <LocaleText k="stats.last30Days" />, value: [dayjs().subtract(29, 'day'), dayjs()] },
]

const StatsPage: React.FC = () => {
  const { t } = useI18n()
  const [range, setRange] = React.useState<[Dayjs, Dayjs]>([dayjs().subtract(6, 'day'), dayjs()])
  const [tokenId, setTokenId] = React.useState<number | undefined>(undefined)
  const [tokens, setTokens] = React.useState<GatewayToken[]>([])
  const [data, setData] = React.useState<StatsResponse | null>(null)
  const [links, setLinks] = React.useState<LinkStat[]>([])
  const [loading, setLoading] = React.useState(true)

  const refresh = React.useCallback(() => {
    setLoading(true)
    const q = { start: range[0].format('YYYY-MM-DD'), end: range[1].format('YYYY-MM-DD'), tokenId }
    Promise.all([myStats(q), myStatsLinks(q)])
      .then(([st, lk]) => {
        setData(st)
        setLinks(lk.links)
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [range, tokenId])

  React.useEffect(refresh, [refresh])

  // 令牌下拉数据源（含已吊销令牌——吊销前可能已有历史用量）
  React.useEffect(() => {
    listTokens()
      .then((r) => setTokens(r.tokens))
      .catch(() => {})
  }, [])

  const groupColumns = (dimName: string) => [
    { title: dimName, dataIndex: 'dim' },
    {
      title: t('stats.requests'),
      dataIndex: 'requests',
      align: 'right' as const,
      render: (v: number) => fmtInt(v),
      sorter: (a: StatsGroup, b: StatsGroup) => a.requests - b.requests,
    },
    {
      title: t('stats.inputTokens'),
      dataIndex: 'promptTokens',
      align: 'right' as const,
      render: (v: number) => fmtInt(v),
      sorter: (a: StatsGroup, b: StatsGroup) => a.promptTokens - b.promptTokens,
    },
    {
      title: t('stats.outputTokens'),
      dataIndex: 'completionTokens',
      align: 'right' as const,
      render: (v: number) => fmtInt(v),
      sorter: (a: StatsGroup, b: StatsGroup) => a.completionTokens - b.completionTokens,
    },
    {
      title: t('stats.estCost'),
      dataIndex: 'cost',
      render: (v: number) => <Money value={v} mode="cost" />,
      align: 'right' as const,
      sorter: (a: StatsGroup, b: StatsGroup) => a.cost - b.cost,
    },
  ]

  return (
    <div>
      <div className="page-heading">
        <div><h2>{t('stats.title')}</h2><p>{t('stats.subtitle')}</p></div>
        <Space>
          <Select
            allowClear
            placeholder={t('stats.token')}
            style={{ width: 180 }}
            options={tokens.map((tk) => ({
              value: tk.id,
              label: tk.revoked ? `${tk.name}（${t('tokens.revoked')}）` : tk.name,
            }))}
            onChange={(v) => setTokenId(v)}
          />
          <DatePicker.RangePicker
            value={range}
            onChange={(v) => {
              if (v && v[0] && v[1]) setRange([v[0], v[1]])
            }}
            disabledDate={(d) => d.isAfter(dayjs(), 'day')}
            presets={rangePresets}
            allowClear={false}
          />
          <Button icon={<ReloadOutlined />} onClick={refresh} loading={loading}>{t('stats.refresh')}</Button>
        </Space>
      </div>
      <Card loading={loading} style={{ marginBottom: 16 }}>
        <div className="stat-strip">
          <div className="stat-cell">
            <Statistic title={t('stats.requests')} value={fmtInt(data?.summary.requests ?? 0)} />
          </div>
          <div className="stat-cell">
            <Statistic title={t('stats.errorRate')} value={data?.summary.errorRate ?? 0} suffix="%" precision={2} />
          </div>
          <div className="stat-cell">
            <Tooltip title={t('stats.tokensTooltip', { input: fmtInt(data?.summary.promptTokens ?? 0), output: fmtInt(data?.summary.completionTokens ?? 0) })}>
              <Statistic title={t('stats.tokensInOut')} value={`${fmtTokens(data?.summary.promptTokens ?? 0)} / ${fmtTokens(data?.summary.completionTokens ?? 0)}`} />
            </Tooltip>
          </div>
          <div className="stat-cell">
            <Statistic title={t('stats.estCost')} value={data?.summary.cost ?? 0} formatter={(v) => <Money value={v as number} mode="cost" big />} />
            {data?.summary.unpriced ? <span className="stat-note">{t('stats.partiallyUnpriced')}</span> : null}
          </div>
        </div>
      </Card>
      <Card title={t('stats.recent')} loading={loading} style={{ marginBottom: 16 }}>
        {data?.recent?.length ? (
          <Table<LatestUsage>
            rowKey="id"
            size="small"
            pagination={false}
            dataSource={data.recent}
            scroll={{ x: 'max-content' }}
            columns={[
              {
                title: t('common.time'),
                dataIndex: 'createdAt',
                width: 180,
                render: (v: number) => <span className="text-tertiary">{formatDateTime(v)}</span>,
              },
              {
                title: t('stats.channel'),
                dataIndex: 'channelName',
                width: 130,
                ellipsis: true,
                render: (v: string, r: LatestUsage) => v || `#${r.channelId}`,
              },
              {
                title: t('stats.line'),
                dataIndex: 'lineUrl',
                width: 260,
                ellipsis: { showTitle: false },
                render: (v: string, r: LatestUsage) => <LineUrl url={v} via={r.via} />,
              },
              {
                title: t('common.model'),
                dataIndex: 'model',
                render: (_, r: LatestUsage) => (
                  <span>
                    <b>{r.model}</b>
                    {r.upstreamModel && r.upstreamModel !== r.model ? (
                      <span className="text-tertiary">{t('stats.upstreamSuffix', { model: r.upstreamModel })}</span>
                    ) : null}
                  </span>
                ),
              },
              {
                title: t('common.status'),
                dataIndex: 'statusCode',
                width: 90,
                render: (v: number) => <Tag color={v < 400 ? 'green' : 'red'}>{v}</Tag>,
              },
            ]}
          />
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('stats.emptyRecent')} />
        )}
      </Card>
      <Card
        title={t('stats.links')}
        extra={<span className="text-tertiary" style={{ fontSize: 12 }}>{t('stats.linksHint')}</span>}
        loading={loading}
        style={{ marginBottom: 16 }}
      >
        {links.length ? (
          <LinksTable links={links} />
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('stats.emptyLinks')} />
        )}
      </Card>
      <Row gutter={16}>
        <Col xs={24} span={12}>
          <Card title={t('stats.byChannel')} loading={loading}>
            <Table<StatsGroup> rowKey="dim" size="small" pagination={false} scroll={{ x: 'max-content' }} dataSource={data?.byChannel ?? []} columns={groupColumns(t('stats.channel'))} />
          </Card>
        </Col>
        <Col xs={24} span={12}>
          <Card title={t('stats.byModel')} loading={loading}>
            <Table<StatsGroup> rowKey="dim" size="small" pagination={false} scroll={{ x: 'max-content' }} dataSource={data?.byModel ?? []} columns={groupColumns(t('common.model'))} />
          </Card>
        </Col>
      </Row>
      <Card title={t('stats.byKey')} style={{ marginTop: 16 }} loading={loading}>
        <Table<StatsGroup> rowKey="dim" size="small" pagination={false} scroll={{ x: 'max-content' }} dataSource={data?.byKey ?? []} columns={groupColumns(t('stats.key'))} />
      </Card>
    </div>
  )
}

export default StatsPage
