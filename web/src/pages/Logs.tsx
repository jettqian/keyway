import React from 'react'
import { Table, Select, Input, InputNumber, message } from 'antd'
import { listLogs, listChannels } from '../api'
import type { LogEntry, Channel } from '../api/types'

const LogsPage: React.FC = () => {
  const [logs, setLogs] = React.useState<LogEntry[]>([])
  const [total, setTotal] = React.useState(0)
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [loading, setLoading] = React.useState(false)
  const [page, setPage] = React.useState(1)
  const [filter, setFilter] = React.useState<{ channelId?: number; model?: string; statusCode?: number }>({})

  const refresh = React.useCallback(() => {
    setLoading(true)
    listLogs({ page, pageSize: 20, ...filter })
      .then((r) => {
        setLogs(r.logs)
        setTotal(r.total)
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [page, filter])

  React.useEffect(refresh, [refresh])

  React.useEffect(() => {
    listChannels()
      .then((r) => setChannels(r.channels))
      .catch(() => {})
  }, [])

  return (
    <div>
      <div className="page-heading"><div><h2>请求日志</h2><p>按渠道、模型或状态码定位请求，回看路由与耗时。</p></div></div>
      <div style={{ display: 'flex', gap: 8, marginBottom: 16, flexWrap: 'wrap' }}>
        <Select
          allowClear
          placeholder="渠道"
          style={{ width: 160 }}
          options={channels.map((c) => ({ value: c.id, label: c.name }))}
          onChange={(v) => setFilter((f) => ({ ...f, channelId: v }))}
        />
        <Input
          allowClear
          placeholder="模型"
          style={{ width: 160 }}
          onChange={(e) => !e.target.value && setFilter((f) => ({ ...f, model: undefined }))}
          onPressEnter={(e) => setFilter((f) => ({ ...f, model: (e.target as HTMLInputElement).value }))}
        />
        <InputNumber
          placeholder="状态码"
          style={{ width: 100 }}
          onChange={(v) => setFilter((f) => ({ ...f, statusCode: typeof v === 'number' ? v : undefined }))}
        />
      </div>
      <Table<LogEntry>
        rowKey="id"
        loading={loading}
        dataSource={logs}
        pagination={{ current: page, total, pageSize: 20, onChange: setPage }}
        columns={[
          { title: '时间', dataIndex: 'createdAt', width: 170 },
          {
            title: '渠道',
            dataIndex: 'channelName',
          },
          { title: '线路', dataIndex: 'lineUrl', ellipsis: true, width: 160 },
          { title: '路径', dataIndex: 'via', width: 110 },
          { title: '协议', dataIndex: 'protocol', width: 90 },
          { title: '模型', dataIndex: 'model', ellipsis: true },
          {
            title: '状态',
            dataIndex: 'statusCode',
            width: 80,
            render: (s: number) => (
              <span style={{ color: s < 400 ? undefined : 'red' }}>{s}</span>
            ),
          },
          { title: '首字节', dataIndex: 'ttftMs', width: 90, render: (v: number) => (v ? `${v}ms` : '-') },
          { title: '总耗时', dataIndex: 'totalMs', width: 90, render: (v: number) => (v ? `${v}ms` : '-') },
          {
            title: 'Tokens',
            width: 140,
            render: (_, l) => (
              <span>
                {l.promptTokens}/{l.completionTokens}
                {l.cachedTokens ? <span style={{ color: '#888' }}>（缓存 {l.cachedTokens}）</span> : null}
              </span>
            ),
          },
          {
            title: '费用',
            width: 90,
            render: (_, l) =>
              l.inputCost == null ? <span style={{ color: '#aaa' }}>未定价</span> : `$${(l.inputCost + (l.outputCost ?? 0)).toFixed(4)}`,
          },
          { title: '错误', dataIndex: 'error', ellipsis: true },
        ]}
      />
    </div>
  )
}

export default LogsPage
