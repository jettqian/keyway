import React from 'react'
import { Card, Col, Row, Space, Statistic, Table, Segmented, Tag, message } from 'antd'
import { myStats } from '../api'
import type { StatsResponse, StatsGroup } from '../api/types'
import { formatDateTime } from '../format'

const StatsPage: React.FC = () => {
  const [days, setDays] = React.useState(7)
  const [data, setData] = React.useState<StatsResponse | null>(null)
  const [loading, setLoading] = React.useState(true)

  React.useEffect(() => {
    setLoading(true)
    myStats(days)
      .then(setData)
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [days])

  const groupColumns = (dimName: string) => [
    { title: dimName, dataIndex: 'dim' },
    { title: '请求数', dataIndex: 'requests', sorter: (a: StatsGroup, b: StatsGroup) => a.requests - b.requests },
    {
      title: '输入 tokens',
      dataIndex: 'promptTokens',
      sorter: (a: StatsGroup, b: StatsGroup) => a.promptTokens - b.promptTokens,
    },
    {
      title: '输出 tokens',
      dataIndex: 'completionTokens',
      sorter: (a: StatsGroup, b: StatsGroup) => a.completionTokens - b.completionTokens,
    },
    {
      title: '费用估算',
      dataIndex: 'cost',
      render: (v: number) => `$${v.toFixed(4)}`,
      sorter: (a: StatsGroup, b: StatsGroup) => a.cost - b.cost,
    },
  ]

  return (
    <div>
      <div className="page-heading">
        <div><h2>用量统计</h2><p>查看请求量、Token 消耗与费用估算。</p></div>
        <Segmented
          options={[
            { label: '今天', value: 1 },
            { label: '近 7 天', value: 7 },
            { label: '近 30 天', value: 30 },
          ]}
          value={days}
          onChange={(v) => setDays(v as number)}
        />
      </div>
      <Card title="最近生效流量" loading={loading} style={{ marginBottom: 16 }}>
        {data?.latest ? (
          <Space size="large" wrap>
            <span>
              渠道：<b>{data.latest.channelName || `#${data.latest.channelId}`}</b>
            </span>
            <span>
              模型：<b>{data.latest.model}</b>
              {data.latest.upstreamModel && data.latest.upstreamModel !== data.latest.model ? (
                <span style={{ color: '#999' }}>（上游 {data.latest.upstreamModel}）</span>
              ) : null}
            </span>
            <span style={{ color: '#999' }}>{formatDateTime(data.latest.createdAt)}</span>
            <Tag color="green">{data.latest.statusCode}</Tag>
          </Space>
        ) : (
          <span style={{ color: '#999' }}>暂无流量</span>
        )}
      </Card>
      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic title="请求数" value={data?.summary.requests ?? 0} />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic title="错误率" value={data?.summary.errorRate ?? 0} suffix="%" precision={2} />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic title="tokens（入/出）" value={`${data?.summary.promptTokens ?? 0} / ${data?.summary.completionTokens ?? 0}`} />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="费用估算"
              value={data?.summary.cost ?? 0}
              prefix="$"
              precision={4}
              suffix={data?.summary.unpriced ? '（部分未定价）' : ''}
            />
          </Card>
        </Col>
      </Row>
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
