import React from 'react'
import dayjs from 'dayjs'
import 'dayjs/locale/zh-cn'
import { setApiTranslator } from '../api/client'
import zh from './zh'
import en from './en'
import type { DictKey } from './zh'

// 轻量 i18n：Context + 扁平 key 字典（zh 为类型源，en 编译期校验完整性），
// t 支持任意字典，调用方以 useI18n().t 取值
export type Locale = 'zh' | 'en'

const STORAGE_KEY = 'kw-locale'

const dicts: Record<Locale, Partial<Record<DictKey, string>>> = { zh, en }

function readSavedLocale(): Locale {
  const saved = localStorage.getItem(STORAGE_KEY)
  return saved === 'en' || saved === 'zh' ? saved : 'zh'
}

interface I18nContextValue {
  locale: Locale
  setLocale: (locale: Locale) => void
  t: (key: DictKey, params?: Record<string, string | number>) => string
}

const I18nContext = React.createContext<I18nContextValue>({
  locale: 'zh',
  setLocale: () => {},
  t: (key) => String(key),
})

export const LocaleProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [locale, setLocale] = React.useState<Locale>(readSavedLocale)

  React.useEffect(() => {
    localStorage.setItem(STORAGE_KEY, locale)
    dayjs.locale(locale === 'zh' ? 'zh-cn' : 'en')
    document.documentElement.lang = locale === 'zh' ? 'zh-CN' : 'en'
  }, [locale])

  const t = React.useCallback((key: DictKey, params?: Record<string, string | number>) => {
    const value = dicts[locale][key] ?? zh[key]
    let text = value !== undefined ? value : String(key)
    if (params) {
      for (const [name, value] of Object.entries(params)) {
        text = text.replace(new RegExp(`\\{${name}\\}`, 'g'), String(value))
      }
    }
    return text
  }, [locale])

  // 供非组件层（api/client.ts）本地化兜底错误文案
  React.useEffect(() => {
    setApiTranslator(t)
  }, [t])

  return <I18nContext.Provider value={{ locale, setLocale, t }}>{children}</I18nContext.Provider>
}

export const useI18n = (): I18nContextValue => React.useContext(I18nContext)
