import axios from 'axios'
import { storage } from '@/lib/storage'

const api = axios.create({
  baseURL: '/api/admin',
  headers: { 'Content-Type': 'application/json' },
})

api.interceptors.request.use((config) => {
  const apiKey = storage.getApiKey()
  if (apiKey) {
    config.headers['x-api-key'] = apiKey
  }
  return config
})

export interface UsageRequest {
  id: number
  time: string
  model: string
  stream: boolean
  status: number
  durationMs: number
  inputTokens: number
  outputTokens: number
  error?: string | null
  costUsd?: number | null
  account?: string
  endpoint?: string
  requestBytes?: number
  stopReason?: string
  cacheReadTokens?: number
  cacheWrite5mTokens?: number
  cacheWrite1hTokens?: number
}

export interface UsageRequestDetail extends UsageRequest {
  inboundHeaders: string
  inboundBody: string
  outboundHeaders: string
  outboundBody: string
  responseBody?: string
  payloadProfile?: string
}

export interface ModelUsage {
  model: string
  requests: number
  errors: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheWrite5mTokens: number
  cacheWrite1hTokens: number
  costUsd: number
}

export interface UsageSummary {
  requests: number
  errors: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheWrite5mTokens: number
  cacheWrite1hTokens: number
  costUsd: number
  unpricedRequests: number
  byModel: ModelUsage[]
}

export interface ModelPrice {
  id: string
  name: string
  aliases: string[]
  inputPerM: number
  outputPerM: number
  /** 空表示按输入价 0.1x */
  cacheReadPerM?: number | null
  /** 空表示按输入价 1.25x */
  cacheWrite5mPerM?: number | null
  /** 空表示按输入价 2x */
  cacheWrite1hPerM?: number | null
}

export async function getUsageRequests(): Promise<UsageRequest[]> {
  const { data } = await api.get<UsageRequest[]>('/usage/requests')
  return data
}

export async function getUsageRequest(id: number): Promise<UsageRequestDetail> {
  const { data } = await api.get<UsageRequestDetail>(`/usage/requests/${id}`)
  return data
}

export async function getUsageSummary(): Promise<UsageSummary> {
  const { data } = await api.get<UsageSummary>('/usage/summary')
  return data
}

export async function getPrices(): Promise<ModelPrice[]> {
  const { data } = await api.get<ModelPrice[]>('/usage/prices')
  return data
}

export async function savePrice(price: ModelPrice): Promise<ModelPrice> {
  const { data } = await api.post<ModelPrice>('/usage/prices', price)
  return data
}

export async function deletePrice(id: string): Promise<void> {
  await api.delete(`/usage/prices/${encodeURIComponent(id)}`)
}

export interface AccountCost {
  credentialId: number
  label: string
  /** 凭据已从配置删除，只剩历史用量 */
  removed: boolean
  /** 人民币成本；null 表示未填，不参与倍率；0 表示免费 */
  costCny: number | null
  /** 按当前单价折算的累计消耗，美元 */
  usageUsd: number
  /** 成本 ¥ ÷ 消耗 $ */
  costRatio: number | null
  requests: number
  errors: number
  unpricedRequests: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheWrite5mTokens: number
  cacheWrite1hTokens: number
}

export interface AccountCostReport {
  accounts: AccountCost[]
  costedAccounts: number
  costCny: number
  costedUsageUsd: number
  usageUsd: number
  costRatio: number | null
}

export async function getAccountCosts(): Promise<AccountCostReport> {
  const { data } = await api.get<AccountCostReport>('/usage/accounts')
  return data
}

export async function setAccountCost(id: number, costCny: number | null): Promise<AccountCostReport> {
  const { data } = await api.put<AccountCostReport>(`/usage/accounts/${id}/cost`, { costCny })
  return data
}

export function formatCNY(value?: number | null): string {
  if (value == null || Number.isNaN(value)) return '—'
  if (value === 0) return '¥0'
  return Math.abs(value) < 0.01 ? `¥${value.toFixed(4)}` : `¥${value.toFixed(2)}`
}

/** 与 claude2api 一致：≥100 取整，≥10 一位小数，≥1 两位，其余四位 */
export function formatCostRatio(value?: number | null): string {
  if (value == null || Number.isNaN(value)) return '—'
  const abs = Math.abs(value)
  const body = abs >= 100 ? value.toFixed(0) : abs >= 10 ? value.toFixed(1) : abs >= 1 ? value.toFixed(2) : value.toFixed(4)
  return `${body}×`
}
