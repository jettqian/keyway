import React from 'react'
import { Card, Form, Input, Button, message, Typography, Divider } from 'antd'
import { Link, useNavigate } from 'react-router-dom'
import { login, publicInfo, feishuLoginUrl } from '../api'
import { useI18n } from '../i18n'

const LoginPage: React.FC = () => {
  const { t } = useI18n()
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
      message.success(t('login.success'))
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
        message.error(t('login.feishuNotConfigured'))
      }
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  return (
    <div className="auth-shell">
      <Card className="auth-card">
        <div className="auth-mark">K</div>
        <Typography.Title level={3} style={{ textAlign: 'center', marginTop: 0 }}>{t('login.title')}</Typography.Title>
        <Form onFinish={onFinish} layout="vertical">
          <Form.Item name="username" label={t('login.username')} rules={[{ required: true, message: t('login.usernameRequired') }]}>
            <Input autoFocus autoComplete="username" />
          </Form.Item>
          <Form.Item name="password" label={t('login.password')} rules={[{ required: true, message: t('login.passwordRequired') }]}>
            <Input.Password autoComplete="current-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={loading}>{t('login.submit')}</Button>
        </Form>
        {feishuEnabled ? (
          <>
            <Divider plain style={{ fontSize: 12 }}>{t('login.or')}</Divider>
            <Button block onClick={feishuLogin}>{t('login.feishuQr')}</Button>
          </>
        ) : null}
        <div style={{ marginTop: 12, textAlign: 'center' }}>
          {t('login.noAccount')}{' '}<Link to="/register">{t('login.signUp')}</Link>
        </div>
      </Card>
    </div>
  )
}

export default LoginPage
