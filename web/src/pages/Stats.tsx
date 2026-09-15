import React from 'react'
import { Button, Card, Col, DatePicker, Empty, Row, Space, Statistic, Table, Tag, message } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { myStats } from '../api'
import type { LatestUsage, StatsResponse, StatsGroup } from '../api/types'
import Money from '../components/Money'
import { formatDateTime, fmtInt } from '../format'

// 快捷时间项（自然日口径）
export const rangePresets: { label: string; value: [Dayjs, Dayjs] }[] = [
  { label: '今天', value: [dayjs(), dayjs()] },
  { label: '昨天', value: [dayjs().subtract(1, 'day'), dayjs().subtract(1, 'day')] },
  { label: '近 7 天', value: [dayjs().subtract(6, 'day'), dayjs()] },
  { label: '近 30 天', value: [dayjs().subtract(29, 'day'), dayjs()] },
]

const StatsPage: React.FC = () => {
  const [range, setRange] = React.useState<[Dayjs, Dayjs]>([dayjs().subtract(6, 'day'), dayjs()])
  const [data, setData] = React.useState<StatsResponse | null>(null)
  const [loading, setLoading] = React.useState(true)

  const refresh = React.useCallback(() => {
    setLoading(true)
    myStats({ start: range[0].format('YYYY-MM-DD'), end: range[1].format('YYYY-MM-DD') })
      .then(setData)
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [range])

  React.useEffect(refresh, [refresh])

  const groupColumns = (dimName: string) => [
    { title: dimName, dataIndex: 'dim' },
    {
      title: '请求数',
      dataIndex: 'requests',
      align: 'right' as const,
      render: (v: number) => fmtInt(v),
      sorter: (a: StatsGroup, b: StatsGroup) => a.requests - b.requests,
    },
    {
      title: '输入 tokens',
      dataIndex: 'promptTokens',
      align: 'right' as const,
      render: (v: number) => fmtInt(v),
      sorter: (a: StatsGroup, b: StatsGroup) => a.promptTokens - b.promptTokens,
    },
    {
      title: '输出 tokens',
      dataIndex: 'completionTokens',
      align: 'right' as const,
      render: (v: number) => fmtInt(v),
      sorter: (a: StatsGroup, b: StatsGroup) => a.completionTokens - b.completionTokens,
    },
    {
      title: '费用估算',
      dataIndex: 'cost',
      render: (v: number) => <Money value={v} mode="cost" />,
      align: 'right' as const,
      sorter: (a: StatsGroup, b: StatsGroup) => a.cost - b.cost,
    },
  ]

  return (
    <div>
      <div className="page-heading">
        <div><h2>用量统计</h2><p>查看请求量、Token 消耗与费用估算。</p></div>
        <Space>
          <DatePicker.RangePicker
            value={range}
            onChange={(v) => {
              if (v && v[0] && v[1]) setRange([v[0], v[1]])
            }}
            disabledDate={(d) => d.isAfter(dayjs(), 'day')}
            presets={rangePresets}
            allowClear={false}
          />
          <Button icon={<ReloadOutlined />} onClick={refresh} loading={loading}>刷新</Button>
        </Space>
      </div>
      <Card loading={loading} style={{ marginBottom: 16 }}>
        <div className="stat-strip">
          <div className="stat-cell">
            <Statistic title="请求数" value={fmtInt(data?.summary.requests ?? 0)} />
          </div>
          <div className="stat-cell">
            <Statistic title="错误率" value={data?.summary.errorRate ?? 0} suffix="%" precision={2} />
          </div>
          <div className="stat-cell">
            <Statistic title="tokens（入/出）" value={`${fmtInt(data?.summary.promptTokens ?? 0)} / ${fmtInt(data?.summary.completionTokens ?? 0)}`} />
          </div>
          <div className="stat-cell">
            <Statistic title="费用估算" value={data?.summary.cost ?? 0} formatter={(v) => <Money value={v as number} mode="cost" big />} />
            {data?.summary.unpriced ? <span className="stat-note">部分未定价</span> : null}
          </div>
        </div>
      </Card>
      <Card title="最近生效流量" loading={loading} style={{ marginBottom: 16 }}>
        {data?.recent?.length ? (
          <Table<LatestUsage>
            rowKey="id"
            size="small"
            pagination={false}
            dataSource={data.recent}
            columns={[
              {
                title: '时间',
                dataIndex: 'createdAt',
                width: 180,
                render: (v: number) => <span className="text-tertiary">{formatDateTime(v)}</span>,
              },
              {
                title: '渠道',
                dataIndex: 'channelName',
                render: (v: string, r: LatestUsage) => v || `#${r.channelId}`,
              },
              {
                title: '线路',
                dataIndex: 'lineUrl',
                ellipsis: true,
                render: (v: string, r: LatestUsage) =>
                  v ? (
                    <span>
                      {v}
                      {r.via ? <span className="text-tertiary"> · {r.via}</span> : null}
                    </span>
                  ) : (
                    <span className="text-tertiary">—</span>
                  ),
              },
              {
                title: '模型',
                dataIndex: 'model',
                render: (_, r: LatestUsage) => (
                  <span>
                    <b>{r.model}</b>
                    {r.upstreamModel && r.upstreamModel !== r.model ? (
                      <span className="text-tertiary">（上游 {r.upstreamModel}）</span>
                    ) : null}
                  </span>
                ),
              },
              {
                title: '状态',
                dataIndex: 'statusCode',
                width: 90,
                render: (v: number) => <Tag color={v < 400 ? 'green' : 'red'}>{v}</Tag>,
              },
            ]}
          />
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="该时间范围内还没有请求" />
        )}
      </Card>
      <Row gutter={16}>
        <Col span={12}>
          <Card title="按渠道" loading={loading}>
            <Table<StatsGroup> rowKey="dim" size="small" pagination={false} dataSource={data?.byChannel ?? []} columns={groupColumns('渠道')} />
          </Card>
        </Col>
        <Col span={12}>
          <Card title="按模型" loading={loading}>
            <Table<StatsGroup> rowKey="dim" size="small" pagination={false} dataSource={data?.byModel ?? []} columns={groupColumns('模型')} />
          </Card>
        </Col>
      </Row>
      <Card title="按密钥（多账号分账）" style={{ marginTop: 16 }} loading={loading}>
        <Table<StatsGroup> rowKey="dim" size="small" pagination={false} dataSource={data?.byKey ?? []} columns={groupColumns('密钥')} />
      </Card>
    </div>
  )
}

export default StatsPage
