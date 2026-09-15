import React from 'react'
import { Card, Form, Input, Button, message, Typography } from 'antd'
import { Link, useNavigate } from 'react-router-dom'
import { register } from '../api'

const RegisterPage: React.FC = () => {
  const nav = useNavigate()
  const [loading, setLoading] = React.useState(false)

  const onFinish = async (values: { username: string; password: string; inviteCode?: string }) => {
    setLoading(true)
    try {
      await register(values.username, values.password, values.inviteCode)
      message.success('注册成功，请登录')
      nav('/login')
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="auth-shell">
      <Card className="auth-card">
        <div className="auth-mark">K</div>
        <Typography.Title level={3} style={{ textAlign: 'center', marginTop: 0 }}>注册 Keyway</Typography.Title>
        <Form onFinish={onFinish} layout="vertical">
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input autoFocus autoComplete="username" />
          </Form.Item>
          <Form.Item
            name="password"
            label="密码"
            rules={[
              { required: true, message: '请输入密码' },
              { min: 8, message: '至少 8 位' },
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="inviteCode" label="邀请码（若启用邀请制）">
            <Input placeholder="开放注册时可留空" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={loading}>注册</Button>
        </Form>
        <div style={{ marginTop: 12, textAlign: 'center' }}>
          已有账号？<Link to="/login">登录</Link>
        </div>
      </Card>
    </div>
  )
}

export default RegisterPage
