import React from 'react'
import ReactDOM from 'react-dom/client'
import { ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import '@fontsource/inter/400.css'
import '@fontsource/inter/500.css'
import '@fontsource/inter/600.css'
import '@fontsource/inter/700.css'
import '@fontsource/inter/800.css'
import App from './App'
import './styles.css'

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <ConfigProvider
      locale={zhCN}
      theme={{
        token: {
          colorPrimary: '#176b87',
          colorInfo: '#176b87',
          colorLink: '#176b87',
          colorSuccess: '#2e7d5b',
          colorError: '#c1443c',
          borderRadius: 8,
          colorBgLayout: '#f4f7f8',
          colorText: '#173042',
          fontFamily: 'Inter, "PingFang SC", "Microsoft YaHei", sans-serif',
        },
        components: {
          Layout: { headerBg: '#ffffff', siderBg: '#ffffff' },
          Table: { headerBg: '#edf3f5', headerColor: '#355461', rowHoverBg: '#f2f8f9' },
          Card: { headerFontSize: 16 },
        },
      }}
    >
      <App />
    </ConfigProvider>
  </React.StrictMode>,
)
