import React from 'react'
import { Table, Select, Input, InputNumber, Popover, Tag, Typography, message } from 'antd'
import { listLogs, listChannels } from '../api'
import type { LogEntry, Channel } from '../api/types'
import Money from '../components/Money'
import LineUrl from '../components/LineUrl'
import { formatDateTime, fmtInt, fmtMs } from '../format'
import { useI18n } from '../i18n'

const LogsPage: React.FC = () => {
  const { t } = useI18n()
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
      <div className="page-heading"><div><h2>{t('logs.title')}</h2><p>{t('logs.subtitle')}</p></div></div>
      <div style={{ display: 'flex', gap: 8, marginBottom: 16, flexWrap: 'wrap' }}>
        <Select
          allowClear
          placeholder={t('logs.channel')}
          style={{ width: 160 }}
          options={channels.map((c) => ({ value: c.id, label: c.name }))}
          onChange={(v) => setFilter((f) => ({ ...f, channelId: v }))}
        />
        <Input
          allowClear
          placeholder={t('common.model')}
          style={{ width: 160 }}
          onChange={(e) => !e.target.value && setFilter((f) => ({ ...f, model: undefined }))}
          onPressEnter={(e) => setFilter((f) => ({ ...f, model: (e.target as HTMLInputElement).value }))}
        />
        <InputNumber
          placeholder={t('logs.statusCode')}
          style={{ width: 100 }}
          onChange={(v) => setFilter((f) => ({ ...f, statusCode: typeof v === 'number' ? v : undefined }))}
        />
      </div>
      <Table<LogEntry>
        rowKey="id"
        loading={loading}
        dataSource={logs}
        scroll={{ x: 'max-content' }}
        pagination={{ current: page, total, pageSize: 20, onChange: setPage }}
        columns={[
          { title: t('common.time'), dataIndex: 'createdAt', width: 170, render: (v: number | string) => formatDateTime(v) },
          {
            title: t('logs.channel'),
            dataIndex: 'channelName',
          },
          { title: t('logs.line'), dataIndex: 'lineUrl', width: 180, ellipsis: { showTitle: false }, render: (v: string) => <LineUrl url={v} /> },
          { title: t('logs.path'), dataIndex: 'via', width: 110 },
          { title: t('logs.protocol'), dataIndex: 'protocol', width: 90 },
          { title: t('common.model'), dataIndex: 'model', ellipsis: true },
          {
            title: t('logs.reasoningEffort'),
            dataIndex: 'reasoningEffort',
            width: 110,
            render: (v?: string | null) => (v ? <Tag color="purple">{v}</Tag> : <span className="text-tertiary">-</span>),
          },
          {
            title: t('common.status'),
            dataIndex: 'statusCode',
            width: 80,
            render: (s: number) => (s >= 400 ? <span className="status-error">{s}</span> : s),
          },
          { title: t('logs.ttft'), dataIndex: 'ttftMs', width: 90, align: 'right', render: (v: number) => fmtMs(v) },
          { title: t('logs.totalTime'), dataIndex: 'totalMs', width: 90, align: 'right', render: (v: number) => fmtMs(v) },
          {
            title: 'Tokens',
            width: 160,
            align: 'right',
            render: (_, l) => (
              <span>
                {fmtInt(l.promptTokens)}/{fmtInt(l.completionTokens)}
                {l.cachedTokens ? <span className="text-tertiary">{t('logs.cachedTokens', { count: fmtInt(l.cachedTokens) })}</span> : null}
              </span>
            ),
          },
          {
            title: t('logs.cost'),
            width: 100,
            align: 'right',
            render: (_, l) =>
              l.inputCost == null ? <span className="text-tertiary">{t('common.unpriced')}</span> : <Money value={l.inputCost + (l.outputCost ?? 0)} mode="cost" />,
          },
          {
            title: t('logs.error'),
            dataIndex: 'error',
            ellipsis: { showTitle: false },
            render: (v?: string) =>
              v ? (
                <Popover
                  trigger="click"
                  placement="topLeft"
                  overlayStyle={{ maxWidth: 560 }}
                  content={
                    <Typography.Paragraph copyable={{ text: v }} className="log-error-detail">
                      {v}
                    </Typography.Paragraph>
                  }
                >
                  <span className="status-error" style={{ cursor: 'pointer' }} title={t('logs.viewFullError')}>
                    {v}
                  </span>
                </Popover>
              ) : (
                <span className="text-tertiary">-</span>
              ),
          },
        ]}
      />
    </div>
  )
}

export default LogsPage
