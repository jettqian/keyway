// 剪贴板复制：优先 Clipboard API；失败或非安全上下文（http/内网 IP）时回退 execCommand，
// 保证内网部署下「一键复制」不弹窗。
// 注意：execCommand 要求在用户手势的同步调用栈内执行（Firefox / 部分内嵌浏览器不认可
// 手势之后 await 网络请求的异步续体），调用方应在点击处理函数内同步调用本函数，
// 不要先 await 再复制
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // 内嵌浏览器常见 "Document is not focused"：聚焦后重试一次
      try {
        window.focus()
        await navigator.clipboard.writeText(text)
        return true
      } catch {
        // 落到 execCommand 回退
      }
    }
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '') // 避免移动端弹出键盘
    ta.style.position = 'fixed'
    ta.style.top = '0'
    ta.style.left = '0'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    const sel = window.getSelection()
    const saved = sel && sel.rangeCount > 0 ? sel.getRangeAt(0) : null
    ta.focus()
    ta.select()
    if (typeof ta.setSelectionRange === 'function') {
      ta.setSelectionRange(0, text.length) // iOS Safari 兼容
    }
    let ok = false
    try {
      ok = document.execCommand('copy')
    } catch {
      ok = false
    }
    document.body.removeChild(ta)
    if (saved && sel) {
      sel.removeAllRanges()
      sel.addRange(saved) // 恢复原有选区
    }
    return ok
  } catch {
    return false
  }
}
