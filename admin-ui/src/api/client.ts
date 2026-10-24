import axios from 'axios'
import { storage } from '@/lib/storage'

/** 管理接口共用的客户端：带上 Admin Key，401 时退出登录 */
export const api = axios.create({
  baseURL: '/api/admin',
  headers: { 'Content-Type': 'application/json' },
})

export const LOGOUT_EVENT = 'kiro-admin-logout'

api.interceptors.request.use((config) => {
  const apiKey = storage.getApiKey()
  if (apiKey) {
    config.headers['x-api-key'] = apiKey
  }
  return config
})

api.interceptors.response.use(undefined, (error) => {
  if (error?.response?.status === 401) {
    storage.removeApiKey()
    window.dispatchEvent(new Event(LOGOUT_EVENT))
  }
  return Promise.reject(error)
})
