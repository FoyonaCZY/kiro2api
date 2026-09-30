import { useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { updateCredential } from '@/api/credentials'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import type { CredentialStatusItem, UpdateCredentialRequest } from '@/types/api'
import { extractErrorMessage } from '@/lib/utils'

function isFullProxyLine(value: string) {
  const text = value.trim()
  if (!text || text.toLowerCase() === 'direct') return false
  const body = text.includes('://') ? text.slice(text.indexOf('://') + 3) : text
  if (body.includes('@')) return true
  return body.split(':').length >= 4
}

interface EditCredentialDialogProps {
  credential: CredentialStatusItem
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function EditCredentialDialog({ credential, open, onOpenChange }: EditCredentialDialogProps) {
  const queryClient = useQueryClient()
  const [email, setEmail] = useState('')
  const [priority, setPriority] = useState('0')
  const [endpoint, setEndpoint] = useState('')
  const [region, setRegion] = useState('')
  const [authRegion, setAuthRegion] = useState('')
  const [apiRegion, setApiRegion] = useState('')
  const [proxyUrl, setProxyUrl] = useState('')
  const [proxyUsername, setProxyUsername] = useState('')
  const [proxyPassword, setProxyPassword] = useState('')
  const [refreshToken, setRefreshToken] = useState('')
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [machineId, setMachineId] = useState('')
  const [claudeBaseUrl, setClaudeBaseUrl] = useState('')
  const [claudeApiKey, setClaudeApiKey] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!open) return
    setEmail(credential.email ?? '')
    setPriority(String(credential.priority))
    setEndpoint(credential.configuredEndpoint ?? '')
    setRegion(credential.region ?? '')
    setAuthRegion(credential.authRegion ?? '')
    setApiRegion(credential.apiRegion ?? '')
    setProxyUrl(credential.proxyUrl ?? '')
    setProxyUsername(credential.proxyUsername ?? '')
    setProxyPassword('')
    setRefreshToken('')
    setClientId(credential.clientId ?? '')
    setClientSecret('')
    setMachineId(credential.machineId ?? '')
    setClaudeBaseUrl(credential.claudeBaseUrl ?? '')
    setClaudeApiKey('')
  }, [open, credential])

  const save = async () => {
    const priorityValue = Number(priority)
    if (!Number.isInteger(priorityValue) || priorityValue < 0) {
      toast.error('优先级必须是非负整数')
      return
    }
    const isClaude = credential.authMethod === 'claude_api'
    const body: UpdateCredentialRequest = {
      email: email.trim(),
      priority: priorityValue,
    }
    if (isClaude) {
      if (claudeBaseUrl.trim()) body.claudeBaseUrl = claudeBaseUrl.trim()
      if (claudeApiKey.trim()) body.claudeApiKey = claudeApiKey.trim()
    } else {
      body.endpoint = endpoint.trim()
      body.region = region.trim()
      body.authRegion = authRegion.trim()
      body.apiRegion = apiRegion.trim()
      body.clientId = clientId.trim()
      body.machineId = machineId.trim()
    }
    const proxyText = proxyUrl.trim()
    if (isFullProxyLine(proxyText)) {
      body.proxy = proxyText
    } else {
      body.proxyUrl = proxyText
      body.proxyUsername = proxyUsername.trim()
      if (proxyPassword.trim()) body.proxyPassword = proxyPassword.trim()
    }
    if (refreshToken.trim()) body.refreshToken = refreshToken.trim()
    if (clientSecret.trim()) body.clientSecret = clientSecret.trim()

    setSaving(true)
    try {
      const result = await updateCredential(credential.id, body)
      await queryClient.invalidateQueries({ queryKey: ['credentials'] })
      toast.success(result.message)
      onOpenChange(false)
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  const isApiKey = credential.authMethod === 'api_key'

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>编辑凭据</DialogTitle>
          <DialogDescription>
            {credential.email || `凭据 #${credential.id}`}。保存后立即生效，不用重启。密码、密钥和 Refresh Token 留空表示不改。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 text-sm">
          <Field label="名称">
            <Input value={email} onChange={(event) => setEmail(event.target.value)} placeholder="邮箱或备注，留空则不显示" />
          </Field>
          <Field label="优先级">
            <Input type="number" min="0" value={priority} onChange={(event) => setPriority(event.target.value)} />
          </Field>
          {credential.authMethod === 'claude_api' ? (
            <>
              <Field label="Claude API 地址">
                <Input
                  value={claudeBaseUrl}
                  onChange={(event) => setClaudeBaseUrl(event.target.value)}
                  placeholder="https://api.anthropic.com"
                  autoComplete="off"
                />
              </Field>
              <Field label="Claude API Key">
                <Input
                  type="password"
                  value={claudeApiKey}
                  onChange={(event) => setClaudeApiKey(event.target.value)}
                  placeholder={credential.hasClaudeApiKey ? '已保存，留空不改' : '必填'}
                  autoComplete="new-password"
                />
              </Field>
            </>
          ) : (
            <>
          <Field label="端点">
            <select
              className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm"
              value={endpoint}
              onChange={(event) => setEndpoint(event.target.value)}
            >
              <option value="">默认</option>
              <option value="krs">krs</option>
              <option value="ide">ide</option>
            </select>
          </Field>
          <Field label="区域">
            <Input value={region} onChange={(event) => setRegion(event.target.value)} placeholder="留空使用全局区域" />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Auth 区域">
              <Input value={authRegion} onChange={(event) => setAuthRegion(event.target.value)} placeholder="留空回退" />
            </Field>
            <Field label="API 区域">
              <Input value={apiRegion} onChange={(event) => setApiRegion(event.target.value)} placeholder="留空回退" />
            </Field>
          </div>
            </>
          )}
          <Field label="代理">
            <Input
              value={proxyUrl}
              onChange={(event) => setProxyUrl(event.target.value)}
              placeholder="socks5://host:port，或整段 host:port:user:pass"
              autoComplete="off"
            />
          </Field>
          <p className="text-xs text-muted-foreground -mt-1">
            留空使用全局代理，填 direct 表示直连。整段粘贴 socks5://host:port:user:pass 时，下面的用户名和密码不用再填。
          </p>
          <div className="grid grid-cols-2 gap-3">
            <Field label="代理用户名">
              <Input value={proxyUsername} onChange={(event) => setProxyUsername(event.target.value)} autoComplete="off" />
            </Field>
            <Field label="代理密码">
              <Input
                type="password"
                value={proxyPassword}
                onChange={(event) => setProxyPassword(event.target.value)}
                placeholder={credential.hasProxyPassword ? '已保存，留空不改' : '可选'}
                autoComplete="new-password"
              />
            </Field>
          </div>
          {!isApiKey && credential.authMethod !== 'claude_api' && (
            <Field label="Refresh Token">
              <Input
                type="password"
                value={refreshToken}
                onChange={(event) => setRefreshToken(event.target.value)}
                placeholder={credential.hasRefreshToken ? '已保存，留空不改' : 'OAuth 凭据需要'}
                autoComplete="new-password"
              />
            </Field>
          )}
          {credential.authMethod === 'idc' && (
            <>
              <Field label="Client ID">
                <Input value={clientId} onChange={(event) => setClientId(event.target.value)} autoComplete="off" />
              </Field>
              <Field label="Client Secret">
                <Input
                  type="password"
                  value={clientSecret}
                  onChange={(event) => setClientSecret(event.target.value)}
                  placeholder={credential.hasClientSecret ? '已保存，留空不改' : ''}
                  autoComplete="new-password"
                />
              </Field>
            </>
          )}
          {credential.authMethod !== 'claude_api' && (
            <Field label="Machine ID">
              <Input value={machineId} onChange={(event) => setMachineId(event.target.value)} placeholder="64 位十六进制或 UUID，留空不改" />
            </Field>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>取消</Button>
          <Button onClick={save} disabled={saving}>{saving ? '保存中' : '保存'}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid gap-1">
      <span className="text-muted-foreground">{label}</span>
      {children}
    </label>
  )
}
