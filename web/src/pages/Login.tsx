import React from 'react'
import { Card, Form, Input, Button, message, Typography } from 'antd'
import { Link, useNavigate } from 'react-router-dom'
import { login } from '../api'

const LoginPage: React.FC = () => {
  const nav = useNavigate()
  const [loading, setLoading] = React.useState(false)

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

  return (
    <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <Card style={{ width: 360 }}>
        <Typography.Title level={3} style={{ textAlign: 'center' }}>Keyway 登录</Typography.Title>
        <Form onFinish={onFinish} layout="vertical">
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input autoFocus />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={loading}>登录</Button>
        </Form>
        <div style={{ marginTop: 12, textAlign: 'center' }}>
          没有账号？<Link to="/register">注册</Link>
        </div>
      </Card>
    </div>
  )
}

export default LoginPage
