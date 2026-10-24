import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { CheckCircle2, Copy, ExternalLink, MinusCircle, XCircle } from 'lucide-react'
import { addCredential, completeSocialLogin, deleteCredential, getCredentialBalance, setCredentialDisabled, startSocialLogin } from '@/api/credentials'
import { parseImport, toAddRequest, type ImportItem } from '@/lib/import'
import { cn, extractErrorMessage, sha256Hex } from '@/lib/utils'
import type { CredentialStatusItem } from '@/types/api'
import { Button } from '@/components/ui/button'
import { Disclosure, Field, Input, Textarea } from '@/components/ui/field'
import { Segmented, Spinner, Switch } from '@/components/ui/controls'
import { Sheet, SheetBody, SheetHeader } from '@/components/ui/surface'

type Tab = 'token' | 'import' | 'login'

export function AddAccountSheet({ open, onOpenChange, existing }: { open: boolean; onOpenChange: (open: boolean) => void; existing: CredentialStatusItem[] }) {
  const [tab, setTab] = useState<Tab>('token')
  const [locked, setLocked] = useState(false)
  return (
    <Sheet open={open} onOpenChange={(next) => { if (!locked) onOpenChange(next) }}>
      <SheetHeader title="添加账号" />
      <div className="px-6 pb-4">
        <Segmented
          className="w-full"
          value={tab}
          onChange={setTab}
          disabled={locked}
          options={[
            { value: 'token', label: '令牌' },
            { value: 'import', label: '批量导入' },
            { value: 'login', label: '代理登录' },
          ]}
        />
      </div>
      <SheetBody>
        {tab === 'token' ? <TokenForm onDone={() => onOpenChange(false)} /> : null}
        {tab === 'import' ? <ImportForm existing={existing} onBusy={setLocked} /> : null}
        {tab === 'login' ? <LoginForm onDone={() => onOpenChange(false)} /> : null}
      </SheetBody>
    </Sheet>
  )
}

type Method = 'social' | 'idc' | 'api_key' | 'claude_api'

const methods: [Method, string][] = [
  ['social', 'Social'],
  ['idc', 'IdC / Builder ID'],
  ['api_key', 'Kiro API Key'],
  ['claude_api', 'Claude API'],
]

function TokenForm({ onDone }: { onDone: () => void }) {
  const queryClient = useQueryClient()
  const [method, setMethod] = useState<Method>('social')
  const [f, setF] = useState({
    refreshToken: '', clientId: '', clientSecret: '', kiroApiKey: '', claudeBaseUrl: 'https://api.anthropic.com', claudeApiKey: '',
    proxyUrl: '', proxyUsername: '', proxyPassword: '', authRegion: '', apiRegion: '', machineId: '', priority: '',
  })
  const [saving, setSaving] = useState(false)
  const set = (key: keyof typeof f) => (event: React.ChangeEvent<HTMLInputElement>) => setF((prev) => ({ ...prev, [key]: event.target.value }))
  const v = (s: string) => s.trim() || undefined

  const ready =
    method === 'claude_api' ? f.claudeBaseUrl.trim() && f.claudeApiKey.trim()
      : method === 'api_key' ? f.kiroApiKey.trim()
        : method === 'idc' ? f.refreshToken.trim() && f.clientId.trim() && f.clientSecret.trim()
          : f.refreshToken.trim()

  const submit = async () => {
    setSaving(true)
    try {
      const res = await addCredential({
        authMethod: method,
        refreshToken: method === 'social' || method === 'idc' ? v(f.refreshToken) : undefined,
        clientId: method === 'idc' ? v(f.clientId) : undefined,
        clientSecret: method === 'idc' ? v(f.clientSecret) : undefined,
        kiroApiKey: method === 'api_key' ? v(f.kiroApiKey) : undefined,
        claudeBaseUrl: method === 'claude_api' ? v(f.claudeBaseUrl) : undefined,
        claudeApiKey: method === 'claude_api' ? v(f.claudeApiKey) : undefined,
        proxyUrl: v(f.proxyUrl),
        proxyUsername: v(f.proxyUsername),
        proxyPassword: v(f.proxyPassword),
        authRegion: method === 'claude_api' ? undefined : v(f.authRegion),
        apiRegion: method === 'claude_api' ? undefined : v(f.apiRegion),
        machineId: method === 'claude_api' ? undefined : v(f.machineId),
        priority: Number.parseInt(f.priority, 10) || 0,
      })
      await queryClient.invalidateQueries({ queryKey: ['credentials'] })
      toast.success(res.email ? `已添加 ${res.email}` : '已添加账号')
      onDone()
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form className="grid gap-4" onSubmit={(event) => { event.preventDefault(); if (ready) submit() }}>
      <div className="grid gap-1.5">
        <span className="text-[13px] text-fg2">类型</span>
        <div className="flex flex-wrap gap-2">
          {methods.map(([value, label]) => (
            <button
              key={value}
              type="button"
              onClick={() => setMethod(value)}
              className={cn('h-8 rounded-[8px] px-3 text-[13px] font-medium transition', method === value ? 'bg-accent text-white' : 'bg-fill text-fg2 hover:text-fg')}
            >
              {label}
            </button>
          ))}
        </div>
      </div>
      {method === 'social' || method === 'idc' ? (
        <Field label="Refresh Token">
          <Input type="password" autoComplete="off" value={f.refreshToken} onChange={set('refreshToken')} autoFocus />
        </Field>
      ) : null}
      {method === 'idc' ? (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Client ID"><Input autoComplete="off" value={f.clientId} onChange={set('clientId')} /></Field>
          <Field label="Client Secret"><Input type="password" autoComplete="off" value={f.clientSecret} onChange={set('clientSecret')} /></Field>
        </div>
      ) : null}
      {method === 'api_key' ? (
        <Field label="Kiro API Key"><Input type="password" autoComplete="off" placeholder="ksk_…" value={f.kiroApiKey} onChange={set('kiroApiKey')} autoFocus /></Field>
      ) : null}
      {method === 'claude_api' ? (
        <>
          <Field label="地址" hint="选中这个账号时请求原样转发，不转成 Kiro">
            <Input autoComplete="off" value={f.claudeBaseUrl} onChange={set('claudeBaseUrl')} />
          </Field>
          <Field label="API Key"><Input type="password" autoComplete="new-password" value={f.claudeApiKey} onChange={set('claudeApiKey')} /></Field>
        </>
      ) : null}
      <Disclosure title="更多设置">
        <Field label="代理" hint="留空用全局代理，填 direct 直连">
          <Input autoComplete="off" placeholder="socks5://host:port" value={f.proxyUrl} onChange={set('proxyUrl')} />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="代理用户名"><Input autoComplete="off" value={f.proxyUsername} onChange={set('proxyUsername')} /></Field>
          <Field label="代理密码"><Input type="password" autoComplete="new-password" value={f.proxyPassword} onChange={set('proxyPassword')} /></Field>
        </div>
        {method !== 'claude_api' ? (
          <>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Auth 区域"><Input placeholder="默认" value={f.authRegion} onChange={set('authRegion')} /></Field>
              <Field label="API 区域"><Input placeholder="默认" value={f.apiRegion} onChange={set('apiRegion')} /></Field>
            </div>
            <Field label="Machine ID"><Input placeholder="留空自动生成" value={f.machineId} onChange={set('machineId')} /></Field>
          </>
        ) : null}
        <Field label="优先级" hint="数字越小越先用"><Input inputMode="numeric" placeholder="0" value={f.priority} onChange={set('priority')} /></Field>
      </Disclosure>
      <Button type="submit" variant="primary" size="md" className="mt-2" disabled={!ready || saving}>
        {saving ? <Spinner className="border-white/40 border-t-white" /> : null}
        添加
      </Button>
    </form>
  )
}

interface Outcome {
  label: string
  status: 'waiting' | 'running' | 'added' | 'duplicate' | 'failed' | 'skipped'
  note?: string
}

function ImportForm({ existing, onBusy }: { existing: CredentialStatusItem[]; onBusy: (busy: boolean) => void }) {
  const queryClient = useQueryClient()
  const [text, setText] = useState('')
  const [skipBroken, setSkipBroken] = useState(true)
  const [outcomes, setOutcomes] = useState<Outcome[] | null>(null)
  const [running, setRunning] = useState(false)

  const parsed = useMemo(() => {
    if (!text.trim()) return { items: [] as ImportItem[], error: '' }
    try {
      return { items: parseImport(text), error: '' }
    } catch (error) {
      return { items: [] as ImportItem[], error: error instanceof SyntaxError ? 'JSON 格式不对' : extractErrorMessage(error) }
    }
  }, [text])
  const broken = parsed.items.filter((item) => item.broken).length

  const update = (i: number, patch: Partial<Outcome>) => setOutcomes((prev) => prev && prev.map((o, j) => (j === i ? { ...o, ...patch } : o)))

  const run = async () => {
    const items = parsed.items
    const known = new Set(existing.flatMap((c) => [c.refreshTokenHash, c.apiKeyHash]).filter(Boolean) as string[])
    setOutcomes(items.map((item, i) => ({ label: item.label || `第 ${i + 1} 个`, status: skipBroken && item.broken ? 'skipped' : 'waiting' })))
    setRunning(true)
    onBusy(true)
    let added = 0
    for (let i = 0; i < items.length; i++) {
      const item = items[i]
      if (skipBroken && item.broken) continue
      update(i, { status: 'running' })
      const hash = await sha256Hex(item.kiroApiKey ?? item.refreshToken ?? '')
      if (known.has(hash)) {
        update(i, { status: 'duplicate', note: '已经有了' })
        continue
      }
      let id: number | null = null
      try {
        const res = await addCredential(toAddRequest(item))
        id = res.credentialId
        await new Promise((r) => setTimeout(r, 1000))
        const balance = await getCredentialBalance(id)
        known.add(hash)
        added++
        update(i, { status: 'added', label: res.email || item.label || `#${id}`, note: `${balance.currentUsage}/${balance.usageLimit}` })
      } catch (error) {
        let note = extractErrorMessage(error)
        if (id != null) {
          // 验活失败的账号不留下
          try {
            await setCredentialDisabled(id, true)
            await deleteCredential(id)
          } catch (rollback) {
            note += `；回滚失败，需要手动删除 #${id}：${extractErrorMessage(rollback)}`
          }
        }
        update(i, { status: 'failed', note })
      }
    }
    setRunning(false)
    onBusy(false)
    await queryClient.invalidateQueries({ queryKey: ['credentials'] })
    toast.success(`导入完成，新增 ${added} 个`)
  }

  if (outcomes) {
    const done = outcomes.filter((o) => o.status !== 'waiting' && o.status !== 'running').length
    return (
      <div className="grid gap-4">
        <div className="flex items-center justify-between text-[15px]">
          <span className="font-medium">{running ? '正在导入并验活' : '导入完成'}</span>
          <span className="tnum text-fg2">{done}/{outcomes.length}</span>
        </div>
        <div className="divide-y divide-[var(--line)] overflow-hidden rounded-xl bg-fill">
          {outcomes.map((o, i) => (
            <div key={i} className="flex items-start gap-3 px-4 py-3">
              <OutcomeIcon status={o.status} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-[15px]">{o.label}</div>
                {o.note ? <div className={o.status === 'failed' ? 'text-[13px] text-bad' : 'text-[13px] text-fg2'}>{o.note}</div> : null}
              </div>
            </div>
          ))}
        </div>
        {!running ? (
          <Button size="md" onClick={() => { setOutcomes(null); setText('') }}>
            再导入一批
          </Button>
        ) : null}
      </div>
    )
  }

  return (
    <div className="grid gap-4">
      <Field label="JSON" hint="支持数组、单个对象，以及 Kiro Account Manager 导出的文件。每个账号添加后会立即验活，失败的不保留。">
        <Textarea
          className="min-h-56"
          spellCheck={false}
          placeholder={'[{ "refreshToken": "…" }, { "refreshToken": "…", "clientId": "…", "clientSecret": "…" }, { "kiroApiKey": "ksk_…" }]'}
          value={text}
          onChange={(event) => setText(event.target.value)}
        />
      </Field>
      {parsed.error ? <p className="text-[13px] text-bad">{parsed.error}</p> : null}
      {parsed.items.length > 0 ? (
        <div className="flex items-center justify-between rounded-xl bg-fill px-4 py-3 text-[15px]">
          <span>识别到 {parsed.items.length} 个账号</span>
          {broken > 0 ? (
            <label className="flex items-center gap-2 text-[13px] text-fg2">
              跳过 {broken} 个异常账号
              <Switch checked={skipBroken} onCheckedChange={setSkipBroken} />
            </label>
          ) : null}
        </div>
      ) : null}
      <Button variant="primary" size="md" disabled={parsed.items.length === 0} onClick={run}>
        导入
      </Button>
    </div>
  )
}

function OutcomeIcon({ status }: { status: Outcome['status'] }) {
  if (status === 'running') return <Spinner className="mt-0.5" />
  if (status === 'added') return <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-ok" />
  if (status === 'failed') return <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-bad" />
  if (status === 'duplicate' || status === 'skipped') return <MinusCircle className="mt-0.5 h-4 w-4 shrink-0 text-fg3" />
  return <span className="mt-1 h-3 w-3 shrink-0 rounded-full border-[1.5px] border-fg3" />
}

function LoginForm({ onDone }: { onDone: () => void }) {
  const queryClient = useQueryClient()
  const [proxy, setProxy] = useState({ url: '', username: '', password: '' })
  const [link, setLink] = useState('')
  const [callback, setCallback] = useState('')
  const [busy, setBusy] = useState(false)

  const start = async () => {
    setBusy(true)
    try {
      const res = await startSocialLogin({ proxyUrl: proxy.url.trim(), proxyUsername: proxy.username.trim() || undefined, proxyPassword: proxy.password.trim() || undefined })
      setLink(res.authorizationUrl)
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setBusy(false)
    }
  }

  const complete = async () => {
    setBusy(true)
    try {
      const res = await completeSocialLogin(callback.trim())
      await queryClient.invalidateQueries({ queryKey: ['credentials'] })
      toast.success(res.email ? `已添加 ${res.email}` : '已添加账号')
      onDone()
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="grid gap-4">
      <p className="text-[13px] text-fg2">账号通过这条 SOCKS5 代理登录，之后也走这条代理。</p>
      <Field label="SOCKS5 代理">
        <Input placeholder="socks5://host:port" value={proxy.url} onChange={(e) => setProxy({ ...proxy, url: e.target.value })} disabled={Boolean(link)} />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="用户名"><Input autoComplete="off" value={proxy.username} onChange={(e) => setProxy({ ...proxy, username: e.target.value })} disabled={Boolean(link)} /></Field>
        <Field label="密码"><Input type="password" autoComplete="new-password" value={proxy.password} onChange={(e) => setProxy({ ...proxy, password: e.target.value })} disabled={Boolean(link)} /></Field>
      </div>
      {!link ? (
        <Button variant="primary" size="md" disabled={!proxy.url.trim() || busy} onClick={start}>
          生成登录链接
        </Button>
      ) : (
        <>
          <div className="grid gap-2 rounded-xl bg-fill p-4">
            <span className="text-[13px] text-fg2">在挂着同一条代理的浏览器里打开，登录后把地址栏的完整地址粘贴到下面。页面打不开不要紧。</span>
            <div className="flex gap-2">
              <Button size="sm" onClick={() => navigator.clipboard.writeText(link).then(() => toast.success('已复制'))}><Copy className="h-3.5 w-3.5" />复制链接</Button>
              <Button size="sm" onClick={() => window.open(link, '_blank', 'noopener')}><ExternalLink className="h-3.5 w-3.5" />打开</Button>
            </div>
          </div>
          <Field label="回调地址">
            <Textarea className="min-h-20" spellCheck={false} placeholder="http://localhost:3128/oauth/callback?code=…" value={callback} onChange={(e) => setCallback(e.target.value)} />
          </Field>
          <Button variant="primary" size="md" disabled={!callback.trim() || busy} onClick={complete}>
            添加账号
          </Button>
        </>
      )}
    </div>
  )
}
