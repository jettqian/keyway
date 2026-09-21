import React from 'react'
import { Table, Select, Input, InputNumber, Popover, Segmented, Typography, message } from 'antd'
import { listLogs, listChannels, listKeys } from '../api'
import type { LogEntry, Channel, ApiKey } from '../api/types'
import Money from '../components/Money'
import LineUrl from '../components/LineUrl'
import { formatDateTime, fmtInt, fmtMs } from '../format'
import { useI18n } from '../i18n'

const LogsPage: React.FC = () => {
  const { t } = useI18n()
  const [logs, setLogs] = React.useState<LogEntry[]>([])
  const [total, setTotal] = React.useState(0)
  const [channels, setChannels] = React.useState<Channel[]>([])
  const [keys, setKeys] = React.useState<ApiKey[]>([])
  const [loading, setLoading] = React.useState(false)
  const [page, setPage] = React.useState(1)
  const [pageSize, setPageSize] = React.useState(20)
  const [failedOnly, setFailedOnly] = React.useState(false)
  const [filter, setFilter] = React.useState<{ channelId?: number; model?: string; statusCode?: number }>({})

  const refresh = React.useCallback(() => {
    setLoading(true)
    listLogs({ page, pageSize, failed: failedOnly || undefined, ...filter })
      .then((r) => {
        setLogs(r.logs)
        setTotal(r.total)
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [page, pageSize, failedOnly, filter])

  React.useEffect(refresh, [refresh])

  React.useEffect(() => {
    listChannels()
      .then((r) => setChannels(r.channels))
      .catch(() => {})
    listKeys()
      .then((r) => setKeys(r.keys))
      .catch(() => {})
  }, [])

  const keyName = (id?: number | null) => {
    if (!id) return null
    const k = keys.find((x) => x.id === id)
    return k ? k.name : `#${id}`
  }

  return (
    <div>
      <div className="page-heading"><div><h2>{t('logs.title')}</h2><p>{t('logs.subtitle')}</p></div></div>
      <div style={{ display: 'flex', gap: 8, marginBottom: 16, flexWrap: 'wrap', alignItems: 'center' }}>
        <Segmented
          value={failedOnly ? 'failed' : 'all'}
          options={[
            { value: 'all', label: t('logs.allLogs') },
            { value: 'failed', label: t('logs.failuresOnly') },
          ]}
          onChange={(v) => {
            setFailedOnly(v === 'failed')
            setPage(1)
          }}
        />
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
        pagination={{
          current: page,
          total,
          pageSize,
          showSizeChanger: true,
          pageSizeOptions: [20, 50, 100],
          // 条数变化回到第 1 页（原实现忽略 size 参数，选择器形同虚设）
          onChange: (p, s) => {
            if (s !== pageSize) {
              setPageSize(s)
              setPage(1)
            } else {
              setPage(p)
            }
          },
        }}
        // 行展开（v1.5.58）：路由与元数据移入展开区，主表 8 列免横向滚动
        expandable={{
          expandedRowRender: (l) => {
            const items: React.ReactNode[] = []
            const meta = (label: string, value: React.ReactNode) => items.push(
              <span key={label} style={{ whiteSpace: 'nowrap' }}>
                <span className="text-tertiary">{label}：</span>
                {value}
              </span>,
            )
            meta(t('logs.line'), <LineUrl url={l.lineUrl} via={l.via} />)
            if (l.upstreamModel && l.upstreamModel !== l.model) meta(t('logs.upstreamModel'), l.upstreamModel)
            if (l.protocol) meta(t('logs.protocol'), l.protocol)
            if (l.reasoningEffort) meta(t('logs.reasoningEffort'), l.reasoningEffort)
            const kn = keyName(l.keyId)
            if (kn) meta(t('logs.key'), kn)
            return (
              <div style={{ padding: '2px 0 6px' }}>
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '4px 24px' }}>{items}</div>
                {l.error ? (
                  <Typography.Paragraph copyable={{ text: l.error }} className="log-error-detail" style={{ marginBottom: 0, marginTop: 6 }}>
                    {l.error}
                  </Typography.Paragraph>
                ) : null}
              </div>
            )
          },
        }}
        columns={[
          { title: t('common.time'), dataIndex: 'createdAt', width: 150, render: (v: number | string) => formatDateTime(v) },
          {
            title: t('logs.channel'),
            dataIndex: 'channelName',
          },
          {
            title: t('common.model'),
            dataIndex: 'model',
            ellipsis: true,
            render: (v: string) => v || '—',
          },
          {
            title: t('common.status'),
            dataIndex: 'statusCode',
            width: 70,
            // 200 但带错误摘要（流内失败，v1.5.54）同样标红——状态码如实反映
            // 上游 200，失败信号在错误列与状态红染上呈现
            render: (s: number, l) => (s >= 400 || l.error ? <span className="status-error">{s}</span> : s),
          },
          {
            // 首字节/总耗时合并一列
            title: t('logs.timing'),
            width: 130,
            render: (_, l) => (
              <span>
                <span className="text-tertiary">{t('logs.ttftShort')}</span>
                {l.ttftMs ? fmtMs(l.ttftMs) : '—'}
                <span className="text-tertiary"> · {t('logs.totalShort')}</span>
                {l.totalMs ? fmtMs(l.totalMs) : '—'}
              </span>
            ),
          },
          {
            title: 'Tokens',
            width: 150,
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
