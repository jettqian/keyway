import React from 'react'
import ReactDOM from 'react-dom/client'
import { ConfigProvider, theme as antdTheme } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import enUS from 'antd/locale/en_US'
import { LocaleProvider, useI18n } from './i18n'
import { ThemeProvider, useTheme, initTheme } from './theme'
import '@fontsource/inter/400.css'
import '@fontsource/inter/500.css'
import '@fontsource/inter/600.css'
import '@fontsource/inter/700.css'
import '@fontsource/inter/800.css'
import App from './App'
import './styles.css'

// 暗色 token：在亮色基调（青墨色系）基础上降低背景亮度、提亮主色与文字，
// 保持与亮色一致的色彩气质
const tokens = (dark: boolean) => ({
  colorPrimary: dark ? '#3f96b0' : '#176b87',
  colorInfo: dark ? '#3f96b0' : '#176b87',
  colorLink: dark ? '#3f96b0' : '#176b87',
  colorSuccess: dark ? '#4e9a78' : '#2e7d5b',
  colorError: dark ? '#e07b74' : '#c1443c',
  borderRadius: 8,
  colorBgLayout: dark ? '#0e1417' : '#f4f7f8',
  colorText: dark ? '#d7e1e7' : '#173042',
  fontFamily: 'Inter, "PingFang SC", "Microsoft YaHei", sans-serif',
})

// 根组件：在 Provider 内部读取当前语言/主题，驱动 antd 的 locale 与算法
const Root: React.FC = () => {
  const { locale } = useI18n()
  const { mode } = useTheme()
  const dark = mode === 'dark'
  return (
    <ConfigProvider
      locale={locale === 'zh' ? zhCN : enUS}
      theme={{
        algorithm: dark ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
        token: tokens(dark),
        components: {
          Layout: { headerBg: dark ? '#121a1f' : '#ffffff', siderBg: dark ? '#121a1f' : '#ffffff' },
          Table: {
            headerBg: dark ? '#162027' : '#edf3f5',
            headerColor: dark ? '#9db3bc' : '#355461',
            rowHoverBg: dark ? '#1a242a' : '#f2f8f9',
          },
          Card: { headerFontSize: 16 },
        },
      }}
    >
      <App />
    </ConfigProvider>
  )
}

// 渲染前同步 data-theme，避免暗色用户刷新时闪一帧亮色
initTheme()

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <LocaleProvider>
      <ThemeProvider>
        <Root />
      </ThemeProvider>
    </LocaleProvider>
  </React.StrictMode>,
)
