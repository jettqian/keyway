// fetch 封装：JSON、携带会话 Cookie 与 CSRF 头
export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

// 非组件层无法使用 i18n hook：由 LocaleProvider 注入翻译函数，
// 用于本地化「请求失败」兜底文案；未注入时回退 HTTP 状态码
type Translator = (key: string, params?: Record<string, string | number>) => string
let translate: Translator | null = null
export function setApiTranslator(fn: Translator): void {
  translate = fn
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    'X-Keyway-CSRF': '1',
  }
  if (init?.body) {
    headers['Content-Type'] = 'application/json'
  }
  const resp = await fetch(path, {
    ...init,
    credentials: 'include',
    headers: { ...headers, ...init?.headers },
  })
  if (resp.status === 204) {
    return undefined as T
  }
  const text = await resp.text()
  const data = text ? JSON.parse(text) : null
  if (!resp.ok) {
    const message =
      data?.error?.message ||
      data?.message ||
      (translate ? translate('common.requestFailed', { status: resp.status }) : `HTTP ${resp.status}`)
    throw new ApiError(message, resp.status)
  }
  return data as T
}

export const get = <T>(path: string) => request<T>(path)
export const post = <T>(path: string, body?: unknown) =>
  request<T>(path, { method: 'POST', body: body !== undefined ? JSON.stringify(body) : undefined })
export const put = <T>(path: string, body?: unknown) =>
  request<T>(path, { method: 'PUT', body: body !== undefined ? JSON.stringify(body) : undefined })
export const del = <T>(path: string) => request<T>(path, { method: 'DELETE' })
