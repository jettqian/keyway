import React from 'react'
import { Card, Form, Input, Button, message, Typography, Divider } from 'antd'
import { Link, useNavigate } from 'react-router-dom'
import { login, publicInfo, feishuLoginUrl } from '../api'

const LoginPage: React.FC = () => {
  const nav = useNavigate()
  const [loading, setLoading] = React.useState(false)
  const [feishuEnabled, setFeishuEnabled] = React.useState(false)

  React.useEffect(() => {
    publicInfo()
      .then((r) => setFeishuEnabled(r.feishuEnabled))
      .catch(() => {})
  }, [])

  const onFinish = async (values: { username: string; password: string }) => {
    setLoading(true)
    try {
      await login(values.username, values.password)
      message.success('登录成功')
      nav('/')
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const feishuLogin = async () => {
    try {
      const r = await feishuLoginUrl()
      if (r.url) {
        window.location.href = r.url
      } else {
        message.error('飞书登录未完成配置')
      }
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div className="auth-shell">
      <Card className="auth-card">
        <div className="auth-mark">K</div>
        <Typography.Title level={3} style={{ textAlign: 'center', marginTop: 0 }}>Keyway 登录</Typography.Title>
        <Form onFinish={onFinish} layout="vertical">
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input autoFocus />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={loading}>登录</Button>
        </Form>
        {feishuEnabled ? (
          <>
            <Divider plain style={{ fontSize: 12 }}>或</Divider>
            <Button block onClick={feishuLogin}>飞书扫码登录</Button>
          </>
        ) : null}
        <div style={{ marginTop: 12, textAlign: 'center' }}>
          没有账号？<Link to="/register">注册</Link>
        </div>
      </Card>
    </div>
  )
}

export default LoginPage
