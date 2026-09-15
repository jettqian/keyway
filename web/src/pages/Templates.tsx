import React from 'react'
import { Table, Button, message } from 'antd'
import { useNavigate } from 'react-router-dom'
import { listTemplates, copyTemplate } from '../api'
import type { ChannelTemplate } from '../api/types'
import { useI18n } from '../i18n'

const TemplatesPage: React.FC = () => {
  const nav = useNavigate()
  const { t } = useI18n()
  const [templates, setTemplates] = React.useState<ChannelTemplate[]>([])
  const [loading, setLoading] = React.useState(true)

  React.useEffect(() => {
    listTemplates()
      .then((r) => setTemplates(r.templates))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  const copy = async (tpl: ChannelTemplate) => {
    try {
      const r = await copyTemplate(tpl.id)
      message.success(t('templates.copiedDraft'))
      nav(`/channels/${r.channel.id}`)
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <div className="page-heading"><div><h2>{t('templates.title')}</h2><p>{t('templates.subtitle')}</p></div></div>
      <Table<ChannelTemplate>
        rowKey="id"
        loading={loading}
        dataSource={templates}
        scroll={{ x: 'max-content' }}
        columns={[
          { title: t('common.name'), dataIndex: 'name' },
          { title: t('templates.baseUrlsCount'), width: 90, align: 'right', render: (_, tpl) => tpl.baseUrls.length },
          { title: t('templates.modelsCount'), width: 90, align: 'right', render: (_, tpl) => tpl.models.length },
          { title: t('templates.copyCount'), dataIndex: 'copyCount', width: 90, align: 'right' },
          { title: t('common.description'), dataIndex: 'note', ellipsis: true },
          {
            title: t('common.action'),
            width: 140,
            render: (_, tpl) => (
              <Button type="primary" size="small" onClick={() => copy(tpl)}>
                {t('templates.copyToChannels')}
              </Button>
            ),
          },
        ]}
        locale={{ emptyText: t('templates.empty') }}
      />
    </div>
  )
}

export default TemplatesPage
