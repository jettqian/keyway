import React from 'react'
import { Table, Button, message } from 'antd'
import { useNavigate } from 'react-router-dom'
import { listTemplates, copyTemplate } from '../api'
import type { ChannelTemplate } from '../api/types'

const TemplatesPage: React.FC = () => {
  const nav = useNavigate()
  const [templates, setTemplates] = React.useState<ChannelTemplate[]>([])
  const [loading, setLoading] = React.useState(true)

  React.useEffect(() => {
    listTemplates()
      .then((r) => setTemplates(r.templates))
      .catch((e) => message.error((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  const copy = async (t: ChannelTemplate) => {
    try {
      const r = await copyTemplate(t.id)
      message.success('已复制为渠道草稿，绑定密钥并启用后生效')
      nav(`/channels/${r.channel.id}`)
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <div className="page-heading"><div><h2>预制模板</h2><p>复制管理员维护的线路配置，绑定密钥后即可快速接入。</p></div></div>
      <Table<ChannelTemplate>
        rowKey="id"
        loading={loading}
        dataSource={templates}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '线路数', width: 90, align: 'right', render: (_, t) => t.baseUrls.length },
          { title: '模型数', width: 90, align: 'right', render: (_, t) => t.models.length },
          { title: '复制次数', dataIndex: 'copyCount', width: 90, align: 'right' },
          { title: '说明', dataIndex: 'note', ellipsis: true },
          {
            title: '操作',
            width: 140,
            render: (_, t) => (
              <Button type="primary" size="small" onClick={() => copy(t)}>
                复制到我的渠道
              </Button>
            ),
          },
        ]}
        locale={{ emptyText: '暂无模板（管理员尚未配置）' }}
      />
    </div>
  )
}

export default TemplatesPage
