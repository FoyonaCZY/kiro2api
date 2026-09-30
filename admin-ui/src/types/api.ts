// 凭据状态响应
export interface CredentialsStatusResponse {
  total: number
  available: number
  currentId: number
  credentials: CredentialStatusItem[]
}

// 单个凭据状态
export interface CredentialStatusItem {
  cooldownRemainingSeconds?: number
  id: number
  priority: number
  disabled: boolean
  failureCount: number
  isCurrent: boolean
  expiresAt: string | null
  authMethod: string | null
  hasProfileArn: boolean
  email?: string
  refreshTokenHash?: string
  apiKeyHash?: string
  maskedApiKey?: string
  successCount: number
  lastUsedAt: string | null
  hasProxy: boolean
  proxyUrl?: string
  refreshFailureCount: number
  disabledReason?: string
  endpoint: string
  configuredEndpoint?: string
  claudeBaseUrl?: string
  hasClaudeApiKey?: boolean
  proxyUsername?: string
  hasProxyPassword?: boolean
  region?: string
  authRegion?: string
  apiRegion?: string
  clientId?: string
  hasClientSecret?: boolean
  hasRefreshToken?: boolean
  machineId?: string
}

// 余额响应
export interface BalanceResponse {
  id: number
  subscriptionTitle: string | null
  currentUsage: number
  usageLimit: number
  remaining: number
  usagePercentage: number
  nextResetAt: number | null
}

// 成功响应
export interface SuccessResponse {
  success: boolean
  message: string
}

// 错误响应
export interface AdminErrorResponse {
  error: {
    type: string
    message: string
  }
}

// 请求类型
export interface SetDisabledRequest {
  disabled: boolean
}

export interface SetPriorityRequest {
  priority: number
}

export interface UpdateCredentialRequest {
  email?: string
  priority?: number
  proxy?: string
  proxyUrl?: string
  proxyUsername?: string
  proxyPassword?: string
  endpoint?: string
  region?: string
  authRegion?: string
  apiRegion?: string
  refreshToken?: string
  clientId?: string
  clientSecret?: string
  machineId?: string
  claudeBaseUrl?: string
  claudeApiKey?: string
}

// 添加凭据请求
export interface AddCredentialRequest {
  refreshToken?: string
  authMethod?: 'social' | 'idc' | 'api_key' | 'claude_api'
  clientId?: string
  clientSecret?: string
  priority?: number
  authRegion?: string
  apiRegion?: string
  machineId?: string
  proxyUrl?: string
  proxyUsername?: string
  proxyPassword?: string
  kiroApiKey?: string
  claudeBaseUrl?: string
  claudeApiKey?: string
  endpoint?: string
}

// 添加凭据响应
export interface AddCredentialResponse {
  success: boolean
  message: string
  credentialId: number
  email?: string
}
