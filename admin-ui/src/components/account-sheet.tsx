import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Minus, Pencil, Plus, RefreshCw } from 'lucide-react'
import { deleteCredential, forceRefreshToken, getCredentialBalance, resetCredentialFailure, setCredentialDisabled, setCredentialPriority, updateCredential } from '@/api/credentials'
import { getAccessGroups, setGroupMembers } from '@/api/access'
import { formatCostRatio, formatCredits, getAccountCosts } from '@/api/usage'
import type { CredentialStatusItem, UpdateCredentialRequest } from '@/types/api'
import { accountName, accountState, methodName, relTime, stateText, stateTone, usd } from '@/lib/format'
import { cn, extractErrorMessage, parseError } from '@/lib/utils'
import { Button, ConfirmButton } from '@/components/ui/button'
import { Badge, Dot, Meter, Spinner, Switch } from '@/components/ui/controls'
import { Disclosure, Field, Input } from '@/components/ui/field'
import { Prop, Props, SectionHeader, Sheet, SheetBody, SheetHeader, Stat } from '@/components/ui/surface'
import { CostInput } from '@/components/cost-input'
import { useGlobalProxy } from '@/components/proxy-sheet'

export function AccountSheet({ credential, onClose }: { credential: CredentialStatusItem | null; onClose: () => void }) {
  const [editing, setEditing] = useState(false)
  useEffect(() => setEditing(false), [credential?.id])
  return (
    <Sheet open={credential != null} onOpenChange={(open) => { if (!open) onClose() }}>
      {credential ? (editing ? <EditView c={credential} onBack={() => setEditing(false)} /> : <DetailView c={credential} onEdit={() => setEditing(true)} onClose={onClose} />) : null}
    </Sheet>
  )
}

function ProxyValue({ c }: { c: CredentialStatusItem }) {
  const global = useGlobalProxy()
  if (c.proxyUrl?.toLowerCase() === 'direct') return <span className="text-[13px]">直连</span>
  if (c.hasProxy) return <span className="break-all text-[13px]">{c.proxyUrl}</span>
  return <span className="break-all text-[13px] text-fg2">跟随全局（{global.data?.proxyUrl || '直连'}）</span>
}

function DetailView({ c, onEdit, onClose }: { c: CredentialStatusItem; onEdit: () => void; onClose: () => void }) {
  const queryClient = useQueryClient()
  const isClaude = c.authMethod === 'claude_api'
  const oauth = !isClaude && c.authMethod !== 'api_key'
  const balance = useQuery({
    queryKey: ['credential-balance', c.id],
    queryFn: () => getCredentialBalance(c.id),
    enabled: !isClaude && !c.disabled,
    retry: false,
    staleTime: 60_000,
  })
  const costs = useQuery({ queryKey: ['usage-accounts'], queryFn: getAccountCosts })
  const cost = costs.data?.accounts.find((row) => row.credentialId === c.id)
  const groups = useQuery({ queryKey: ['access-groups'], queryFn: getAccessGroups })
  const [pending, setPending] = useState<string | null>(null)

  const act = async (key: string, fn: () => Promise<unknown>, ok: string) => {
    setPending(key)
    try {
      await fn()
      await queryClient.invalidateQueries({ queryKey: ['credentials'] })
      toast.success(ok)
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setPending(null)
    }
  }

  const joined = (groups.data ?? []).filter((g) => g.members.includes(c.id))
  const toggleGroup = (id: string) => {
    const group = groups.data?.find((g) => g.id === id)
    if (!group) return
    const members = group.members.includes(c.id) ? group.members.filter((m) => m !== c.id) : [...group.members, c.id]
    setGroupMembers(id, members)
      .then(() => queryClient.invalidateQueries({ queryKey: ['access-groups'] }))
      .catch((error) => toast.error(extractErrorMessage(error)))
  }

  const state = accountState(c)
  const b = balance.data
  const used = b ? b.usagePercentage : 0
  const balanceError = balance.error ? parseError(balance.error) : null

  return (
    <>
      <SheetHeader
        title={accountName(c)}
        subtitle={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="flex items-center gap-1.5"><Dot tone={stateTone[state]} />{stateText(c)}</span>
            <span>{methodName(c.authMethod)}</span>
            {c.isCurrent ? <Badge tone="accent">当前使用</Badge> : null}
          </span>
        }
      >
        <label className="mt-1 flex items-center gap-2 text-[13px] text-fg2">
          启用
          <Switch
            label="启用"
            checked={!c.disabled}
            disabled={pending === 'toggle'}
            onCheckedChange={(on) => act('toggle', () => setCredentialDisabled(c.id, !on), on ? '已启用' : '已停用')}
          />
        </label>
      </SheetHeader>
      <SheetBody>
        <div className="flex flex-wrap gap-2">
          {oauth ? (
            <Button disabled={c.disabled || pending === 'refresh'} onClick={() => act('refresh', () => forceRefreshToken(c.id), '已刷新令牌')}>
              <RefreshCw className={cn('h-3.5 w-3.5', pending === 'refresh' && 'animate-spin')} />
              刷新令牌
            </Button>
          ) : null}
          {state === 'failing' ? (
            <Button disabled={pending === 'reset'} onClick={() => act('reset', () => resetCredentialFailure(c.id), '已清零失败次数')}>
              清零失败次数
            </Button>
          ) : null}
          <Button onClick={onEdit}>
            <Pencil className="h-3.5 w-3.5" />
            编辑
          </Button>
          <ConfirmButton
            className="ml-auto"
            onConfirm={() =>
              act('delete', async () => {
                if (!c.disabled) await setCredentialDisabled(c.id, true)
                await deleteCredential(c.id)
                onClose()
              }, '已删除')
            }
          >
            删除
          </ConfirmButton>
        </div>

        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Stat label="成功请求" value={c.successCount.toLocaleString()} note={relTime(c.lastUsedAt)} />
          <Stat label="消耗" value={usd(cost?.usageUsd ?? 0)} note="按当前单价" />
          <Stat label="Credits" value={formatCredits(cost?.credits ?? 0)} note="上游扣除" />
          <Stat label="成本倍率" value={formatCostRatio(cost?.costRatio)} note="成本 ÷ 消耗" />
        </div>

        {!isClaude ? (
          <section>
            <SectionHeader title="额度">
              <Button variant="plain" size="iconXs" aria-label="刷新额度" disabled={balance.isFetching || c.disabled} onClick={() => balance.refetch()}>
                <RefreshCw className={cn('h-3.5 w-3.5', balance.isFetching && 'animate-spin')} />
              </Button>
            </SectionHeader>
            <div className="space-y-2.5 rounded-xl bg-fill px-4 py-3.5">
              {c.disabled ? (
                <p className="text-fg2">启用后才能查询额度</p>
              ) : balance.isLoading ? (
                <div className="flex items-center gap-2 text-fg2"><Spinner />查询中</div>
              ) : balanceError ? (
                <div>
                  <p className="text-bad">{balanceError.title}</p>
                  {balanceError.detail ? <p className="mt-1 text-[13px] text-fg2">{balanceError.detail}</p> : null}
                </div>
              ) : b ? (
                <>
                  <div className="flex items-baseline justify-between gap-3">
                    <span className="font-medium">{b.subscriptionTitle || '未知套餐'}</span>
                    <span className="tnum text-[13px] text-fg2">剩余 {b.remaining.toLocaleString(undefined, { maximumFractionDigits: 2 })}</span>
                  </div>
                  <Meter value={used} tone={used > 90 ? 'bad' : used > 70 ? 'warn' : 'accent'} />
                  <div className="tnum flex justify-between text-[12px] text-fg2">
                    <span>已用 {b.currentUsage.toLocaleString(undefined, { maximumFractionDigits: 2 })} / {b.usageLimit.toLocaleString()}（{used.toFixed(1)}%）</span>
                    {b.nextResetAt ? <span>{new Date(b.nextResetAt * 1000).toLocaleDateString()} 重置</span> : null}
                  </div>
                </>
              ) : null}
            </div>
          </section>
        ) : null}

        <section>
          <SectionHeader title="设置" />
          <Props>
            <Prop label="优先级">
              <div className="flex items-center gap-1">
                <Button size="iconXs" aria-label="提前" disabled={c.priority === 0 || pending === 'priority'} onClick={() => act('priority', () => setCredentialPriority(c.id, Math.max(0, c.priority - 1)), '已调整优先级')}>
                  <Minus className="h-3.5 w-3.5" />
                </Button>
                <span className="tnum w-8 text-center">{c.priority}</span>
                <Button size="iconXs" aria-label="延后" disabled={pending === 'priority'} onClick={() => act('priority', () => setCredentialPriority(c.id, c.priority + 1), '已调整优先级')}>
                  <Plus className="h-3.5 w-3.5" />
                </Button>
                <span className="ml-2 text-[12px] text-fg3">数字越小越先用</span>
              </div>
            </Prop>
            <Prop label="分组">
              <div className="flex flex-wrap gap-1.5">
                {(groups.data ?? []).map((g) => {
                  const implicit = g.isDefault && joined.length === 0
                  const active = g.members.includes(c.id) || implicit
                  return (
                    <button
                      key={g.id}
                      type="button"
                      disabled={implicit}
                      onClick={() => toggleGroup(g.id)}
                      className={cn('h-7 rounded-[7px] px-2.5 text-[12px] font-medium transition', active ? 'bg-accent text-white' : 'bg-fill text-fg2 hover:text-fg')}
                    >
                      {g.name}
                    </button>
                  )
                })}
              </div>
            </Prop>
            <Prop label="成本">
              <CostInput credentialId={c.id} value={cost?.costCny ?? null} align="left" />
            </Prop>
            {c.claudeBaseUrl ? <Prop label="地址"><span className="break-all">{c.claudeBaseUrl}</span></Prop> : null}
            {c.maskedApiKey ? <Prop label="API Key"><span className="font-mono text-[13px]">{c.maskedApiKey}</span></Prop> : null}
            <Prop label="代理"><ProxyValue c={c} /></Prop>
          </Props>
        </section>
      </SheetBody>
    </>
  )
}

function isFullProxyLine(value: string) {
  const text = value.trim()
  if (!text || text.toLowerCase() === 'direct') return false
  const body = text.includes('://') ? text.slice(text.indexOf('://') + 3) : text
  return body.includes('@') || body.split(':').length >= 4
}

function EditView({ c, onBack }: { c: CredentialStatusItem; onBack: () => void }) {
  const queryClient = useQueryClient()
  const isClaude = c.authMethod === 'claude_api'
  const [f, setF] = useState({
    email: c.email ?? '',
    proxyUrl: c.proxyUrl ?? '',
    proxyUsername: c.proxyUsername ?? '',
    proxyPassword: '',
    refreshToken: '',
    clientId: c.clientId ?? '',
    clientSecret: '',
    machineId: c.machineId ?? '',
    region: c.region ?? '',
    authRegion: c.authRegion ?? '',
    apiRegion: c.apiRegion ?? '',
    claudeBaseUrl: c.claudeBaseUrl ?? '',
    claudeApiKey: '',
  })
  const [saving, setSaving] = useState(false)
  const set = (key: keyof typeof f) => (event: React.ChangeEvent<HTMLInputElement>) => setF((prev) => ({ ...prev, [key]: event.target.value }))
  const keep = '已保存，留空不改'

  const save = async () => {
    const body: UpdateCredentialRequest = { email: f.email.trim() }
    if (isClaude) {
      if (f.claudeBaseUrl.trim()) body.claudeBaseUrl = f.claudeBaseUrl.trim()
      if (f.claudeApiKey.trim()) body.claudeApiKey = f.claudeApiKey.trim()
    } else {
      body.region = f.region.trim()
      body.authRegion = f.authRegion.trim()
      body.apiRegion = f.apiRegion.trim()
      body.clientId = f.clientId.trim()
      body.machineId = f.machineId.trim()
    }
    const proxy = f.proxyUrl.trim()
    if (isFullProxyLine(proxy)) {
      body.proxy = proxy
    } else {
      body.proxyUrl = proxy
      body.proxyUsername = f.proxyUsername.trim()
      if (f.proxyPassword.trim()) body.proxyPassword = f.proxyPassword.trim()
    }
    if (f.refreshToken.trim()) body.refreshToken = f.refreshToken.trim()
    if (f.clientSecret.trim()) body.clientSecret = f.clientSecret.trim()
    setSaving(true)
    try {
      await updateCredential(c.id, body)
      await queryClient.invalidateQueries({ queryKey: ['credentials'] })
      toast.success('已保存')
      onBack()
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <SheetHeader title="编辑" subtitle={accountName(c)} />
      <SheetBody>
        <form className="grid gap-4" onSubmit={(event) => { event.preventDefault(); save() }}>
          <Field label="名称"><Input value={f.email} onChange={set('email')} placeholder="邮箱或备注" /></Field>
          {isClaude ? (
            <>
              <Field label="地址"><Input autoComplete="off" value={f.claudeBaseUrl} onChange={set('claudeBaseUrl')} /></Field>
              <Field label="API Key"><Input type="password" autoComplete="new-password" placeholder={c.hasClaudeApiKey ? keep : ''} value={f.claudeApiKey} onChange={set('claudeApiKey')} /></Field>
            </>
          ) : null}
          <Field label="代理" hint="留空用全局代理，填 direct 直连，也可以整段粘贴 host:port:user:pass">
            <Input autoComplete="off" placeholder="socks5://host:port" value={f.proxyUrl} onChange={set('proxyUrl')} />
          </Field>
          {!isFullProxyLine(f.proxyUrl) ? (
            <div className="grid grid-cols-2 gap-3">
              <Field label="代理用户名"><Input autoComplete="off" value={f.proxyUsername} onChange={set('proxyUsername')} /></Field>
              <Field label="代理密码"><Input type="password" autoComplete="new-password" placeholder={c.hasProxyPassword ? keep : ''} value={f.proxyPassword} onChange={set('proxyPassword')} /></Field>
            </div>
          ) : null}
          {!isClaude && c.authMethod !== 'api_key' ? (
            <Field label="Refresh Token"><Input type="password" autoComplete="new-password" placeholder={c.hasRefreshToken ? keep : ''} value={f.refreshToken} onChange={set('refreshToken')} /></Field>
          ) : null}
          {c.authMethod === 'idc' ? (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Client ID"><Input autoComplete="off" value={f.clientId} onChange={set('clientId')} /></Field>
              <Field label="Client Secret"><Input type="password" autoComplete="new-password" placeholder={c.hasClientSecret ? keep : ''} value={f.clientSecret} onChange={set('clientSecret')} /></Field>
            </div>
          ) : null}
          {!isClaude ? (
            <Disclosure title="更多设置">
              <Field label="区域"><Input placeholder="默认" value={f.region} onChange={set('region')} /></Field>
              <div className="grid grid-cols-2 gap-3">
                <Field label="Auth 区域"><Input placeholder="默认" value={f.authRegion} onChange={set('authRegion')} /></Field>
                <Field label="API 区域"><Input placeholder="默认" value={f.apiRegion} onChange={set('apiRegion')} /></Field>
              </div>
              <Field label="Machine ID"><Input value={f.machineId} onChange={set('machineId')} /></Field>
            </Disclosure>
          ) : null}
          <div className="mt-2 flex gap-2">
            <Button size="md" className="flex-1" onClick={onBack} disabled={saving}>取消</Button>
            <Button type="submit" size="md" variant="primary" className="flex-1" disabled={saving}>保存</Button>
          </div>
        </form>
      </SheetBody>
    </>
  )
}
