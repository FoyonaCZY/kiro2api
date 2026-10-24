import type { AddCredentialRequest } from '@/types/api'

/** 导入的一条账号，已经统一成扁平结构 */
export interface ImportItem {
  label?: string
  refreshToken?: string
  clientId?: string
  clientSecret?: string
  authRegion?: string
  apiRegion?: string
  machineId?: string
  kiroApiKey?: string
  priority?: number
  /** Account Manager 里标成 error 的账号 */
  broken: boolean
}

function str(v: unknown): string | undefined {
  return typeof v === 'string' && v.trim() ? v.trim() : undefined
}

function normalize(raw: unknown): ImportItem | null {
  if (!raw || typeof raw !== 'object') return null
  const o = raw as Record<string, unknown>
  // Account Manager 旧格式把令牌放在 credentials 里，新格式和普通 JSON 是平铺的
  const c = o.credentials && typeof o.credentials === 'object' ? { ...o, ...(o.credentials as Record<string, unknown>) } : o
  const item: ImportItem = {
    label: str(o.email) ?? str(o.nickname) ?? str(o.label),
    refreshToken: str(c.refreshToken),
    clientId: str(c.clientId),
    clientSecret: str(c.clientSecret),
    authRegion: str(c.authRegion) ?? str(c.region),
    apiRegion: str(c.apiRegion),
    machineId: str(o.machineId) ?? str(c.machineId),
    kiroApiKey: str(c.kiroApiKey),
    priority: typeof c.priority === 'number' ? c.priority : undefined,
    broken: o.status === 'error',
  }
  return item.refreshToken || item.kiroApiKey ? item : null
}

/** 支持单个对象、数组、Account Manager 导出的 { accounts: [...] } */
export function parseImport(text: string): ImportItem[] {
  const parsed = JSON.parse(text) as unknown
  let raw: unknown[]
  if (Array.isArray(parsed)) raw = parsed
  else if (parsed && typeof parsed === 'object' && Array.isArray((parsed as { accounts?: unknown }).accounts)) raw = (parsed as { accounts: unknown[] }).accounts
  else raw = [parsed]
  const items = raw.map(normalize).filter((x): x is ImportItem => x !== null)
  if (items.length === 0) throw new Error('没找到 refreshToken 或 kiroApiKey')
  return items
}

export function toAddRequest(item: ImportItem): AddCredentialRequest {
  if (item.kiroApiKey) {
    return {
      authMethod: 'api_key',
      kiroApiKey: item.kiroApiKey,
      priority: item.priority ?? 0,
      authRegion: item.authRegion,
      apiRegion: item.apiRegion,
      machineId: item.machineId,
    }
  }
  if (Boolean(item.clientId) !== Boolean(item.clientSecret)) {
    throw new Error('IdC 账号要同时有 clientId 和 clientSecret')
  }
  return {
    authMethod: item.clientId ? 'idc' : 'social',
    refreshToken: item.refreshToken,
    clientId: item.clientId,
    clientSecret: item.clientSecret,
    priority: item.priority ?? 0,
    authRegion: item.authRegion,
    apiRegion: item.apiRegion,
    machineId: item.machineId,
  }
}
