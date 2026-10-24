import type { CredentialStatusItem } from '@/types/api'

export function relTime(iso?: string | null): string {
  if (!iso) return '从未'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '—'
  const s = Math.round((Date.now() - t) / 1000)
  if (s < 45) return '刚刚'
  if (s < 3600) return `${Math.round(s / 60)} 分钟前`
  if (s < 86400) return `${Math.round(s / 3600)} 小时前`
  if (s < 86400 * 7) return `${Math.round(s / 86400)} 天前`
  return new Date(t).toLocaleDateString()
}

export function clock(iso: string): string {
  const d = new Date(iso)
  const today = new Date()
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  return d.toDateString() === today.toDateString() ? time : `${d.toLocaleDateString([], { month: 'numeric', day: 'numeric' })} ${time}`
}

/** 12.3k / 1.2M，适合 token 数 */
export function compact(n?: number | null): string {
  if (n == null) return '—'
  const abs = Math.abs(n)
  if (abs >= 1e9) return `${+(n / 1e9).toFixed(2)}B`
  if (abs >= 1e6) return `${+(n / 1e6).toFixed(2)}M`
  if (abs >= 1e4) return `${+(n / 1e3).toFixed(1)}k`
  return n.toLocaleString()
}

export function usd(v?: number | null): string {
  if (v == null || Number.isNaN(v)) return '—'
  if (v === 0) return '$0'
  return Math.abs(v) < 1 ? `$${v.toFixed(4)}` : `$${v.toFixed(2)}`
}

export function duration(ms: number): string {
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`
}

export type AccountState = 'ok' | 'cooling' | 'failing' | 'off'

export function accountState(c: CredentialStatusItem): AccountState {
  if (c.disabled) return 'off'
  if ((c.cooldownRemainingSeconds ?? 0) > 0) return 'cooling'
  if (c.failureCount > 0 || c.refreshFailureCount > 0) return 'failing'
  return 'ok'
}

export const stateTone = { ok: 'ok', cooling: 'warn', failing: 'bad', off: 'off' } as const

export function stateText(c: CredentialStatusItem): string {
  switch (accountState(c)) {
    case 'off':
      return c.disabledReason ? `已停用：${c.disabledReason}` : '已停用'
    case 'cooling':
      return `限流冷却 ${c.cooldownRemainingSeconds} 秒`
    case 'failing':
      return c.refreshFailureCount > 0 ? `刷新失败 ${c.refreshFailureCount} 次` : `失败 ${c.failureCount} 次`
    default:
      return '正常'
  }
}

export function methodName(m?: string | null): string {
  switch (m) {
    case 'social':
      return 'Social'
    case 'idc':
      return 'IdC'
    case 'api_key':
      return 'API Key'
    case 'claude_api':
      return 'Claude API'
    default:
      return m || '未知'
  }
}

export function accountName(c: { id: number; email?: string }): string {
  return c.email || `账号 #${c.id}`
}
