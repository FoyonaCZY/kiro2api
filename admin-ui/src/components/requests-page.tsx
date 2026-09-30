import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getUsageRequest, getUsageRequests, type UsageRequest, type UsageRequestDetail } from '@/api/usage'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { extractErrorMessage } from '@/lib/utils'

type Tab = 'chat' | 'inbound' | 'outbound' | 'diff'
type StatusFilter = 'all' | 'ok' | 'err'

function money(value: number | null | undefined) {
  if (value == null) return '未定价'
  return `$${value.toFixed(4)}`
}

function duration(ms: number) {
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(1)} s`
}

function pretty(text: string) {
  const trimmed = text.trim()
  if (!trimmed) return ''
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2)
  } catch {
    return text
  }
}

function statusClass(status: number) {
  if (status >= 500) return 'text-destructive'
  if (status >= 400) return 'text-amber-700 dark:text-amber-300'
  return 'text-emerald-700 dark:text-emerald-300'
}

function textOf(value: unknown): string {
  if (typeof value === 'string') return value
  if (Array.isArray(value)) {
    return value.map((item) => {
      if (typeof item === 'string') return item
      if (!item || typeof item !== 'object') return String(item ?? '')
      const block = item as { type?: string; text?: string; thinking?: string; name?: string; input?: unknown; content?: unknown }
      if (block.type === 'thinking' || block.thinking) return block.thinking || block.text || ''
      if (block.type === 'tool_use') return `工具 ${block.name || ''}\n${pretty(JSON.stringify(block.input ?? ''))}`
      if (block.type === 'tool_result') return textOf(block.content ?? block.text ?? '')
      if (block.text) return block.text
      return pretty(JSON.stringify(item))
    }).filter(Boolean).join('\n\n')
  }
  if (value && typeof value === 'object') return pretty(JSON.stringify(value))
  return ''
}

function conversation(detail: UsageRequestDetail) {
  const turns: { role: string; label: string; text: string; fold: boolean }[] = []
  let inbound: { system?: unknown; messages?: { role?: string; content?: unknown }[] } | null = null
  try {
    inbound = JSON.parse(detail.inboundBody)
  } catch {
    inbound = null
  }
  const push = (role: string, label: string, text: string) => {
    turns.push({ role, label, text, fold: role === 'system' || text.length > 500 })
  }
  if (inbound?.system) push('system', '系统', textOf(inbound.system))
  for (const message of inbound?.messages ?? []) {
    const role = message.role || 'user'
    push(role, role === 'assistant' ? '助手' : role === 'user' ? '用户' : role, textOf(message.content))
  }
  const response = detail.responseBody?.trim() ?? ''
  if (response) {
    let parsed: { content?: unknown } | null = null
    try {
      parsed = JSON.parse(response)
    } catch {
      parsed = null
    }
    const text = parsed?.content ? textOf(parsed.content) : response
    push('assistant', '最终响应', text)
  }
  return turns
}

function headerMap(raw: string) {
  try {
    const value = JSON.parse(raw) as unknown
    if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, String(item ?? '')]))
  } catch {
    return {}
  }
}

function headerDiff(leftRaw: string, rightRaw: string) {
  const left = headerMap(leftRaw)
  const right = headerMap(rightRaw)
  const keys = [...new Set([...Object.keys(left), ...Object.keys(right)])].sort()
  return keys.map((key) => ({
    key,
    left: left[key] ?? '',
    right: right[key] ?? '',
    changed: (left[key] ?? '') !== (right[key] ?? ''),
  }))
}

export function RequestsPage() {
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [account, setAccount] = useState('all')
  const [status, setStatus] = useState<StatusFilter>('all')
  const { data, isLoading, error, refetch, isFetching } = useQuery({
    queryKey: ['usage-requests'],
    queryFn: getUsageRequests,
  })
  const accounts = useMemo(() => [...new Set((data ?? []).map((row) => row.account).filter(Boolean) as string[])], [data])
  const rows = (data ?? []).filter((row) => {
    if (account !== 'all' && row.account !== account) return false
    if (status === 'ok' && row.status >= 400) return false
    if (status === 'err' && row.status < 400) return false
    return true
  })

  return (
    <div className="space-y-5">
      <PageHeader title="请求" description="最近 200 条。点一行打开详情，对话、原文和请求头对照都在窗口里。">
        <div className="flex flex-wrap items-center gap-2">
          <select className="h-9 rounded-md border bg-background px-2 text-sm" value={account} onChange={(event) => setAccount(event.target.value)}>
            <option value="all">全部账号</option>
            {accounts.map((item) => <option key={item} value={item}>{item}</option>)}
          </select>
          <select className="h-9 rounded-md border bg-background px-2 text-sm" value={status} onChange={(event) => setStatus(event.target.value as StatusFilter)}>
            <option value="all">全部状态</option>
            <option value="ok">成功</option>
            <option value="err">失败</option>
          </select>
          <Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>刷新</Button>
        </div>
      </PageHeader>
      {isLoading ? <p className="text-sm text-muted-foreground">加载中</p> : null}
      {error ? <p className="text-sm text-destructive">{extractErrorMessage(error)}</p> : null}
      <div className="overflow-x-auto rounded-md border">
        <table className="w-full min-w-[1120px] text-left text-sm">
          <thead className="border-b bg-card text-muted-foreground">
            <tr>
              {['时间', '账号', '模型', '方式', '状态', '耗时', '输入', '缓存读', '缓存写', '输出', '费用', ''].map((label) => (
                <th key={label || 'action'} className="px-3 py-2 font-medium">{label}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.id} className="cursor-pointer border-b last:border-0 hover:bg-secondary/40" onClick={() => setSelectedId(row.id)}>
                <td className="whitespace-nowrap px-3 py-2">{new Date(row.time).toLocaleString()}</td>
                <td className="px-3 py-2">{row.account || '—'}</td>
                <td className="px-3 py-2">{row.model}</td>
                <td className="px-3 py-2">{row.stream ? '流式' : '非流式'}</td>
                <td className={`px-3 py-2 ${statusClass(row.status)}`}>{row.status}{row.error ? <span className="ml-2 font-normal text-muted-foreground">{row.error}</span> : null}</td>
                <td className="px-3 py-2">{duration(row.durationMs)}</td>
                <td className="px-3 py-2">{row.inputTokens}</td>
                <td className="px-3 py-2">{row.cacheReadTokens || '—'}</td>
                <td className="px-3 py-2">{cacheWrite(row) || '—'}</td>
                <td className="px-3 py-2">{row.outputTokens}</td>
                <td className="px-3 py-2">{money(row.costUsd)}</td>
                <td className="px-3 py-2 text-right">
                  <Button size="sm" variant="outline" onClick={(event) => { event.stopPropagation(); setSelectedId(row.id) }}>详情</Button>
                </td>
              </tr>
            ))}
            {data && rows.length === 0 ? (
              <tr><td className="px-3 py-8 text-muted-foreground" colSpan={12}>没有符合筛选的请求。</td></tr>
            ) : null}
          </tbody>
        </table>
      </div>
      <RequestDialog id={selectedId} row={data?.find((item) => item.id === selectedId)} onClose={() => setSelectedId(null)} />
    </div>
  )
}

function cacheWrite(row: UsageRequest) {
  return (row.cacheWrite5mTokens ?? 0) + (row.cacheWrite1hTokens ?? 0)
}

function cacheWriteLabel(row: UsageRequest) {
  const h1 = row.cacheWrite1hTokens ?? 0
  return h1 ? `${cacheWrite(row)}（1h ${h1}）` : String(cacheWrite(row))
}

function RequestDialog({ id, row, onClose }: { id: number | null; row?: UsageRequest; onClose: () => void }) {
  const [tab, setTab] = useState<Tab>('chat')
  const detail = useQuery({
    queryKey: ['usage-request', id],
    queryFn: () => getUsageRequest(id as number),
    enabled: id != null,
  })
  const turns = detail.data ? conversation(detail.data) : []
  const diff = detail.data ? headerDiff(detail.data.inboundHeaders, detail.data.outboundHeaders) : []

  return (
    <Dialog open={id != null} onOpenChange={(open) => { if (!open) { onClose(); setTab('chat') } }}>
      <DialogContent className="flex max-h-[88vh] w-[min(1080px,calc(100vw-2rem))] max-w-none flex-col gap-4 overflow-hidden">
        <DialogHeader>
          <DialogTitle>{row?.model ? `请求详情 · ${row.model}` : '请求详情'}</DialogTitle>
        </DialogHeader>
        {row ? (
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-7">
            {[
              ['状态', String(row.status)],
              ['耗时', duration(row.durationMs)],
              ['输入', String(row.inputTokens)],
              ['缓存读', String(row.cacheReadTokens ?? 0)],
              ['缓存写', cacheWriteLabel(row)],
              ['输出', String(row.outputTokens)],
              ['费用', money(row.costUsd)],
            ].map(([label, value]) => (
              <div key={label} className="rounded-md border bg-card px-3 py-2">
                <div className="text-xs text-muted-foreground">{label}</div>
                <div className="mt-1 text-sm font-medium">{value}</div>
              </div>
            ))}
          </div>
        ) : null}
        {row?.error ? <p className="text-sm text-destructive">{row.error}</p> : null}
        {detail.data?.payloadProfile ? (
          <pre className="max-h-40 overflow-auto rounded-md border bg-card p-3 font-mono text-xs leading-5">{detail.data.payloadProfile}</pre>
        ) : null}
        {detail.isLoading ? <p className="text-sm text-muted-foreground">正在读取</p> : null}
        {detail.error ? <p className="text-sm text-destructive">{extractErrorMessage(detail.error)}</p> : null}
        {detail.data ? (
          <>
            <div className="flex flex-wrap gap-2">
              {([
                ['chat', '对话'],
                ['inbound', '入站'],
                ['outbound', '出站'],
                ['diff', '请求头对照'],
              ] as const).map(([key, label]) => (
                <button key={key} type="button" className={`rounded-md px-3 py-1.5 text-sm ${tab === key ? 'bg-secondary' : 'text-muted-foreground hover:bg-secondary/60'}`} onClick={() => setTab(key)}>
                  {label}
                </button>
              ))}
            </div>
            <div className="min-h-0 flex-1 overflow-auto pr-1">
              {tab === 'chat' ? (
                <div className="space-y-3">
                  {turns.length === 0 ? <p className="text-sm text-muted-foreground">没有解析出 messages。到「入站」看原文。</p> : null}
                  {turns.map((turn, index) => <Bubble key={index} label={turn.label} text={turn.text} folded={turn.fold} />)}
                  {!detail.data.responseBody?.trim() ? <p className="text-sm text-muted-foreground">这条记录没有保存最终响应。这版上线之后的新请求才会有。</p> : null}
                </div>
              ) : null}
              {tab === 'inbound' ? <Raw title="入站请求头" text={pretty(detail.data.inboundHeaders)} bodyTitle="入站请求体" body={detail.data.inboundBody} /> : null}
              {tab === 'outbound' ? <Raw title="出站请求头" text={pretty(detail.data.outboundHeaders)} bodyTitle="出站请求体" body={detail.data.outboundBody} /> : null}
              {tab === 'diff' ? (
                diff.length === 0 ? <p className="text-sm text-muted-foreground">没有可对照的请求头。</p> : (
                  <table className="w-full text-left text-xs">
                    <thead className="text-muted-foreground"><tr><th className="py-2 pr-3">头</th><th className="py-2 pr-3">入站</th><th className="py-2">出站</th></tr></thead>
                    <tbody>
                      {diff.map((item) => (
                        <tr key={item.key} className={`border-t align-top ${item.changed ? 'bg-secondary/50' : ''}`}>
                          <td className="py-2 pr-3 font-medium">{item.key}</td>
                          <td className="py-2 pr-3"><pre className="whitespace-pre-wrap">{item.left || '—'}</pre></td>
                          <td className="py-2"><pre className="whitespace-pre-wrap">{item.right || '—'}</pre></td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )
              ) : null}
            </div>
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function Bubble({ label, text, folded }: { label: string; text: string; folded: boolean }) {
  const [open, setOpen] = useState(!folded)
  const preview = text.split('\n')[0]?.slice(0, 80) || '空'
  return (
    <article className="rounded-md border bg-background p-3">
      <button type="button" className="flex w-full items-center justify-between gap-3 text-left" onClick={() => setOpen((value) => !value)}>
        <span className="text-xs text-muted-foreground">{label}</span>
        <span className="truncate text-xs text-muted-foreground">{open ? '收起' : preview}</span>
      </button>
      {open ? <pre className="mt-2 whitespace-pre-wrap font-sans text-sm leading-6">{text || '空'}</pre> : null}
    </article>
  )
}

function Raw({ title, text, bodyTitle, body }: { title: string; text: string; bodyTitle: string; body: string }) {
  return (
    <div className="space-y-4">
      <section>
        <h3 className="mb-2 text-sm font-medium">{title}</h3>
        <pre className="overflow-auto rounded-md border bg-background p-3 font-mono text-xs leading-5">{text || '没有记录'}</pre>
      </section>
      <section>
        <h3 className="mb-2 text-sm font-medium">{bodyTitle}</h3>
        <pre className="overflow-auto rounded-md border bg-background p-3 font-mono text-xs leading-5">{pretty(body) || '没有记录'}</pre>
      </section>
    </div>
  )
}
