import React from 'react'
import { Table, Button, Tag, message, Modal } from 'antd'
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
      message.success('已复制为渠道草稿，请绑定密钥后保存')
      nav(`/channels/${r.channel.id}`)
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div>
      <h2>预制模板</h2>
      <p style={{ color: '#888' }}>管理员维护的渠道模板（线路/代理/模型清单）。复制后完全独立，可自由修改。</p>
      <Table<ChannelTemplate>
        rowKey="id"
        loading={loading}
        dataSource={templates}
        columns={[
          { title: '名称', dataIndex: 'name' },
          {
            title: '类型',
            dataIndex: 'type',
            width: 100,
            render: (t: string) => <Tag color={t === 'anthropic' ? 'purple' : 'geekblue'}>{t}</Tag>,
          },
          { title: '线路数', width: 90, render: (_, t) => t.baseUrls.length },
          { title: '模型数', width: 90, render: (_, t) => t.models.length },
          { title: '复制次数', dataIndex: 'copyCount', width: 90 },
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
      <Modal open={false} title="" footer={null} />
    </div>
  )
}

export default TemplatesPage
