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

export interface AccessKey {
  id: number
  name: string
  secret: string
  prefix: string
  groupId: string
  groupName: string
  disabled: boolean
}

export interface AccessGroup {
  id: string
  name: string
  keyCount: number
  members: number[]
  isDefault: boolean
}

export async function getAccessKeys(): Promise<AccessKey[]> {
  const { data } = await api.get<AccessKey[]>('/access/keys')
  return data
}

export async function createAccessKey(body: { name: string; secret?: string; groupId?: string }): Promise<AccessKey> {
  const { data } = await api.post<AccessKey>('/access/keys', body)
  return data
}

export async function updateAccessKey(id: number, body: { name?: string; groupId?: string; disabled?: boolean }): Promise<void> {
  await api.put(`/access/keys/${id}`, body)
}

export async function deleteAccessKey(id: number): Promise<void> {
  await api.delete(`/access/keys/${id}`)
}

export async function getAccessGroups(): Promise<AccessGroup[]> {
  const { data } = await api.get<AccessGroup[]>('/access/groups')
  return data
}

export async function createAccessGroup(name: string): Promise<AccessGroup> {
  const { data } = await api.post<AccessGroup>('/access/groups', { name })
  return data
}

export async function renameAccessGroup(id: string, name: string): Promise<void> {
  await api.put(`/access/groups/${encodeURIComponent(id)}`, { name })
}

export async function deleteAccessGroup(id: string): Promise<void> {
  await api.delete(`/access/groups/${encodeURIComponent(id)}`)
}

export async function setGroupMembers(id: string, credentialIds: number[]): Promise<void> {
  await api.put(`/access/groups/${encodeURIComponent(id)}/members`, { credentialIds })
}
