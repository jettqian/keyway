import React from 'react'
import { Card, Form, Input, Button, message, Typography } from 'antd'
import { Link, useNavigate } from 'react-router-dom'
import { register } from '../api'
import { useI18n } from '../i18n'

const RegisterPage: React.FC = () => {
  const { t } = useI18n()
  const nav = useNavigate()
  const [loading, setLoading] = React.useState(false)

  const onFinish = async (values: { username: string; password: string; inviteCode?: string }) => {
    setLoading(true)
    try {
      await register(values.username, values.password, values.inviteCode)
      message.success(t('register.success'))
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
        <Typography.Title level={3} style={{ textAlign: 'center', marginTop: 0 }}>{t('register.title')}</Typography.Title>
        <Form onFinish={onFinish} layout="vertical">
          <Form.Item name="username" label={t('register.username')} rules={[{ required: true, message: t('register.usernameRequired') }]}>
            <Input autoFocus autoComplete="username" />
          </Form.Item>
          <Form.Item
            name="password"
            label={t('register.password')}
            rules={[
              { required: true, message: t('register.passwordRequired') },
              { min: 8, message: t('register.passwordMinLength', { min: 8 }) },
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="inviteCode" label={t('register.inviteCode')}>
            <Input placeholder={t('register.inviteCodePlaceholder')} />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={loading}>{t('register.submit')}</Button>
        </Form>
        <div style={{ marginTop: 12, textAlign: 'center' }}>
          {t('register.haveAccount')}{' '}<Link to="/login">{t('register.logIn')}</Link>
        </div>
      </Card>
    </div>
  )
}

export default RegisterPage
