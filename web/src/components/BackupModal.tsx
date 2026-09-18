import React from 'react'
import dayjs from 'dayjs'
import { Modal, Tabs, Radio, Alert, Button, Upload, message, Row, Col, Statistic, Space, Typography } from 'antd'
import { DownloadOutlined, InboxOutlined, ImportOutlined } from '@ant-design/icons'
import { importConfig } from '../api'
import type { ConfigImportResult } from '../api/types'
import { useI18n } from '../i18n'

// 用户配置备份弹窗：导出（完整/纯结构双模式）与导入（合并模式）
const BackupModal: React.FC<{ open: boolean; onClose: () => void }> = ({ open, onClose }) => {
  const { t } = useI18n()
  const [mode, setMode] = React.useState<'full' | 'structure'>('full')
  const [exporting, setExporting] = React.useState(false)
  const [file, setFile] = React.useState<{ name: string; content: string } | null>(null)
  const [importing, setImporting] = React.useState(false)
  const [result, setResult] = React.useState<ConfigImportResult | null>(null)

  React.useEffect(() => {
    if (open) {
      setFile(null)
      setResult(null)
      setExporting(false)
      setImporting(false)
    }
  }, [open])

  // 导出为文件下载：GET 无需 CSRF 头，手动 fetch 拿 blob（client 封装按 JSON 解析，不适用）
  const doExport = async () => {
    setExporting(true)
    try {
      const resp = await fetch(`/api/config/export?secrets=${mode === 'full' ? '1' : '0'}`, { credentials: 'include' })
      if (!resp.ok) {
        let msg = `HTTP ${resp.status}`
        try {
          const data = await resp.json()
          msg = data?.message || msg
        } catch {
          /* 保持状态码兜底 */
        }
        throw new Error(msg)
      }
      const blob = await resp.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `keyway-config-${mode}-${dayjs().format('YYYYMMDD-HHmm')}.json`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch (e) {
      message.error((e as Error).message || t('backup.exportFailed'))
    } finally {
      setExporting(false)
    }
  }

  const doImport = async () => {
    if (!file) return
    setImporting(true)
    try {
      let content = file.content
      // 去 UTF-8 BOM（部分编辑器习惯性写入）
      if (content.charCodeAt(0) === 0xfeff) {
        content = content.slice(1)
      }
      let parsed: unknown
      try {
        parsed = JSON.parse(content)
      } catch {
        message.error(t('backup.invalidJson'))
        return
      }
      const r = await importConfig(parsed)
      setResult(r.result)
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setImporting(false)
    }
  }

  const stats: Array<[string, number]> = result
    ? [
        [t('backup.keysCreated'), result.keysCreated],
        [t('backup.keysReused'), result.keysReused],
        [t('backup.channelsCreated'), result.channelsCreated],
        [t('backup.channelsDrafted'), result.channelsDrafted],
        [t('backup.tokensCreated'), result.tokensCreated],
      ]
    : []
  if (result) {
    if (result.keysMissing > 0) {
      stats.push([t('backup.keysMissing'), result.keysMissing])
    }
    const skipped = result.channelsSkipped + result.tokensSkipped
    if (skipped > 0) {
      stats.push([t('backup.skipped'), skipped])
    }
  }

  return (
    <Modal open={open} onCancel={onClose} footer={null} title={t('backup.title')} width={560}>
      <Tabs
        items={[
          {
            key: 'export',
            label: t('backup.tabExport'),
            children: (
              <div className="backup-pane">
                <Radio.Group value={mode} onChange={(e) => setMode(e.target.value as 'full' | 'structure')}>
                  <Space direction="vertical" size={2}>
                    <Radio value="full">
                      <div className="backup-opt">
                        <div>{t('backup.modeFull')}</div>
                        <Typography.Text type="secondary" className="backup-opt-desc">
                          {t('backup.modeFullDesc')}
                        </Typography.Text>
                      </div>
                    </Radio>
                    <Radio value="structure">
                      <div className="backup-opt">
                        <div>{t('backup.modeStructure')}</div>
                        <Typography.Text type="secondary" className="backup-opt-desc">
                          {t('backup.modeStructureDesc')}
                        </Typography.Text>
                      </div>
                    </Radio>
                  </Space>
                </Radio.Group>
                {mode === 'full' ? (
                  <Alert type="warning" showIcon message={t('backup.fullWarning')} />
                ) : (
                  <Alert type="info" showIcon message={t('backup.structureHint')} />
                )}
                <Button type="primary" icon={<DownloadOutlined />} loading={exporting} onClick={doExport}>
                  {t('backup.exportBtn')}
                </Button>
              </div>
            ),
          },
          {
            key: 'import',
            label: t('backup.tabImport'),
            children: (
              <div className="backup-pane">
                <Alert type="info" showIcon message={t('backup.importHint')} />
                <Upload.Dragger
                  accept=".json,application/json"
                  maxCount={1}
                  fileList={file ? [{ uid: 'import-file', name: file.name, status: 'done' }] : []}
                  onRemove={() => {
                    setFile(null)
                    setResult(null)
                  }}
                  beforeUpload={async (f) => {
                    try {
                      const content = await f.text()
                      setFile({ name: f.name, content })
                      setResult(null)
                    } catch {
                      message.error(t('backup.readFailed'))
                    }
                    return false
                  }}
                >
                  <p className="ant-upload-drag-icon"><InboxOutlined /></p>
                  <p className="ant-upload-text">{t('backup.selectFile')}</p>
                  <p className="ant-upload-hint">{t('backup.fileHint')}</p>
                </Upload.Dragger>
                <Button type="primary" icon={<ImportOutlined />} disabled={!file} loading={importing} onClick={doImport}>
                  {t('backup.importBtn')}
                </Button>
                {result && (
                  <div className="backup-result">
                    <Alert
                      type={result.warnings.length > 0 ? 'warning' : 'success'}
                      showIcon
                      message={t('backup.importDone')}
                    />
                    <Row gutter={[12, 12]}>
                      {stats.map(([title, value]) => (
                        <Col xs={12} sm={8} key={title}>
                          <Statistic title={title} value={value} />
                        </Col>
                      ))}
                    </Row>
                    {result.warnings.length > 0 && (
                      <Alert
                        type="warning"
                        message={t('backup.warnings')}
                        description={
                          <ul style={{ margin: 0, paddingLeft: 18 }}>
                            {result.warnings.map((w, i) => (
                              <li key={i}>{w}</li>
                            ))}
                          </ul>
                        }
                      />
                    )}
                    <Space>
                      <Button type="primary" onClick={() => window.location.reload()}>
                        {t('backup.reloadPage')}
                      </Button>
                      <Button onClick={onClose}>{t('backup.done')}</Button>
                    </Space>
                  </div>
                )}
              </div>
            ),
          },
        ]}
      />
    </Modal>
  )
}

export default BackupModal
