// 亮暗主题：mode 持久化到 localStorage，并同步 <html data-theme> 供 CSS 变量切换；
// 首次访问跟随系统 prefers-color-scheme
import React from 'react'

export type ThemeMode = 'light' | 'dark'

const STORAGE_KEY = 'kw-theme'

interface ThemeContextValue {
  mode: ThemeMode
  setMode: (mode: ThemeMode) => void
  toggle: () => void
}

const ThemeContext = React.createContext<ThemeContextValue>({
  mode: 'light',
  setMode: () => {},
  toggle: () => {},
})

function readSavedMode(): ThemeMode {
  const saved = localStorage.getItem(STORAGE_KEY)
  if (saved === 'light' || saved === 'dark') return saved
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

// 在 React 渲染前同步设置 data-theme，避免暗色用户刷新时闪一帧亮色
export function initTheme(): void {
  document.documentElement.dataset.theme = readSavedMode()
}

export const ThemeProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [mode, setMode] = React.useState<ThemeMode>(readSavedMode)

  React.useEffect(() => {
    document.documentElement.dataset.theme = mode
    localStorage.setItem(STORAGE_KEY, mode)
  }, [mode])

  const toggle = React.useCallback(() => setMode((m) => (m === 'light' ? 'dark' : 'light')), [])

  return <ThemeContext.Provider value={{ mode, setMode, toggle }}>{children}</ThemeContext.Provider>
}

export const useTheme = (): ThemeContextValue => React.useContext(ThemeContext)
