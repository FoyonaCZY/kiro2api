import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { formatCredits, getUsageRequest, getUsageRequests, type UsageRequest, type UsageRequestDetail } from '@/api/usage'
import { clock, compact, duration, usd } from '@/lib/format'
import { cn, extractErrorMessage } from '@/lib/utils'
import { PageTitle } from '@/components/shell'
import { Button } from '@/components/ui/button'
import { Badge, Dot, Segmented, Spinner } from '@/components/ui/controls'
import { Select } from '@/components/ui/field'
import { Empty, EmptyRow, Sheet, SheetBody, SheetHeader, Stat, Table, Td, Th, THead, Tr } from '@/components/ui/surface'

type Status = 'all' | 'ok' | 'err'

export function RequestsPage() {
  const { data, isLoading, error, refetch, isFetching } = useQuery({ queryKey: ['usage-requests'], queryFn: getUsageRequests })
  const [account, setAccount] = useState('')
  const [status, setStatus] = useState<Status>('all')
  const [openId, setOpenId] = useState<number | null>(null)
  const accounts = useMemo(() => [...new Set((data ?? []).map((r) => r.account).filter(Boolean) as string[])].sort(), [data])
  const rows = (data ?? []).filter((r) => (!account || r.account === account) && (status === 'all' || (status === 'ok' ? r.status < 400 : r.status >= 400)))

  return (
    <>
      <PageTitle title="请求" subtitle="最近 200 条">
        <Button onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn('h-3.5 w-3.5', isFetching && 'animate-spin')} />
          刷新
        </Button>
      </PageTitle>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Segmented value={status} onChange={setStatus} options={[{ value: 'all', label: '全部' }, { value: 'ok', label: '成功' }, { value: 'err', label: '失败' }]} />
        {accounts.length > 1 ? (
          <div className="w-full sm:w-56">
            <Select className="h-8 text-[13px]" value={account} onChange={(e) => setAccount(e.target.value)}>
              <option value="">全部账号</option>
              {accounts.map((a) => <option key={a} value={a}>{a}</option>)}
            </Select>
          </div>
        ) : null}
      </div>
      {isLoading ? <div className="flex justify-center py-24"><Spinner className="h-6 w-6" /></div> : null}
      {error ? <Empty title={`读取失败：${extractErrorMessage(error)}`} /> : null}
      {data ? (
        <Table minWidth={900}>
          <THead>
            <Th>时间</Th>
            <Th>模型</Th>
            <Th className="hidden lg:table-cell">账号</Th>
            <Th>状态</Th>
            <Th className="text-right">输入</Th>
            <Th className="text-right">输出</Th>
            <Th className="hidden text-right xl:table-cell">缓存读</Th>
            <Th className="hidden text-right lg:table-cell">费用</Th>
            <Th className="text-right">Credits</Th>
            <Th className="text-right">耗时</Th>
          </THead>
          <tbody>
            {rows.length === 0 ? <EmptyRow colSpan={10}>{data.length === 0 ? '还没有请求' : '没有符合条件的请求'}</EmptyRow> : null}
            {rows.map((r) => {
              const failed = r.status >= 400
              return (
                <Tr key={r.id} onClick={() => setOpenId(r.id)}>
                  <Td className="tnum text-fg2">{clock(r.time)}</Td>
                  <Td>
                    <span className="font-medium">{r.model}</span>
                    {r.stream ? null : <Badge className="ml-2">非流式</Badge>}
                  </Td>
                  <Td className="hidden max-w-[220px] truncate text-fg2 lg:table-cell">{r.account || '—'}</Td>
                  <Td className="max-w-[260px]">
                    <span className={cn('flex items-center gap-2 truncate', failed ? 'text-bad' : 'text-fg2')} title={r.error ?? undefined}>
                      <Dot tone={failed ? 'bad' : 'ok'} />
                      <span className="tnum">{r.status}</span>
                      {failed && r.error ? <span className="truncate">{r.error}</span> : null}
                    </span>
                  </Td>
                  <Td className="tnum text-right">{compact(r.inputTokens)}</Td>
                  <Td className="tnum text-right">{compact(r.outputTokens)}</Td>
                  <Td className="tnum hidden text-right text-fg2 xl:table-cell">{compact(r.cacheReadTokens ?? 0)}</Td>
                  <Td className="tnum hidden text-right lg:table-cell">{usd(r.costUsd)}</Td>
                  <Td className="tnum text-right text-fg2">{formatCredits(r.credits)}</Td>
                  <Td className="tnum text-right text-fg2">{duration(r.durationMs)}</Td>
                </Tr>
              )
            })}
          </tbody>
        </Table>
      ) : null}
      <RequestSheet row={data?.find((r) => r.id === openId)} onClose={() => setOpenId(null)} />
    </>
  )
}

type Tab = 'chat' | 'in' | 'out' | 'headers'

function RequestSheet({ row, onClose }: { row?: UsageRequest; onClose: () => void }) {
  const [tab, setTab] = useState<Tab>('chat')
  const detail = useQuery({ queryKey: ['usage-request', row?.id], queryFn: () => getUsageRequest(row!.id), enabled: row != null })
  const d = detail.data
  return (
    <Sheet wide open={row != null} onOpenChange={(open) => { if (!open) { onClose(); setTab('chat') } }}>
      {row ? (
        <>
          <SheetHeader
            title={row.model}
            subtitle={
              <span className="flex flex-wrap gap-x-3">
                <span className={row.status >= 400 ? 'text-bad' : 'text-ok'}>{row.status}</span>
                <span>{new Date(row.time).toLocaleString()}</span>
                {row.account ? <span>{row.account}</span> : null}
              </span>
            }
          />
          <SheetBody>
            <div className="grid grid-cols-3 gap-2 sm:grid-cols-6">
              {[
                ['输入', compact(row.inputTokens)],
                ['输出', compact(row.outputTokens)],
                ['缓存读', compact(row.cacheReadTokens ?? 0)],
                ['缓存写', compact((row.cacheWrite5mTokens ?? 0) + (row.cacheWrite1hTokens ?? 0))],
                ['费用', usd(row.costUsd)],
                ['Credits', formatCredits(row.credits)],
              ].map(([label, value]) => (
                <Stat key={label} label={label} value={value} />
              ))}
            </div>
            {row.error ? <p className="rounded-xl bg-fill px-4 py-3 text-bad">{row.error}</p> : null}
            <Segmented
              value={tab}
              onChange={setTab}
              options={[{ value: 'chat', label: '对话' }, { value: 'in', label: '入站' }, { value: 'out', label: '出站' }, { value: 'headers', label: '请求头' }]}
            />
            {detail.isLoading ? <div className="flex justify-center py-12"><Spinner /></div> : null}
            {d ? (
              <>
                {tab === 'chat' ? <Conversation d={d} /> : null}
                {tab === 'in' ? <Raw blocks={[['请求头', pretty(d.inboundHeaders)], ['请求体', pretty(d.inboundBody)]]} /> : null}
                {tab === 'out' ? <Raw blocks={[['结构', d.payloadProfile ?? ''], ['请求头', pretty(d.outboundHeaders)], ['请求体', pretty(d.outboundBody)]]} /> : null}
                {tab === 'headers' ? <HeaderDiff d={d} /> : null}
              </>
            ) : null}
          </SheetBody>
        </>
      ) : null}
    </Sheet>
  )
}

function pretty(text: string) {
  const trimmed = text?.trim() ?? ''
  if (!trimmed) return ''
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2)
  } catch {
    return text
  }
}

function textOf(value: unknown): string {
  if (typeof value === 'string') return value
  if (Array.isArray(value)) {
    return value
      .map((item) => {
        if (typeof item === 'string') return item
        if (!item || typeof item !== 'object') return String(item ?? '')
        const b = item as { type?: string; text?: string; thinking?: string; name?: string; input?: unknown; content?: unknown }
        if (b.type === 'thinking' || b.thinking) return b.thinking || b.text || ''
        if (b.type === 'tool_use') return `调用 ${b.name || ''}\n${pretty(JSON.stringify(b.input ?? ''))}`
        if (b.type === 'tool_result') return textOf(b.content ?? b.text ?? '')
        if (b.text) return b.text
        return pretty(JSON.stringify(item))
      })
      .filter(Boolean)
      .join('\n\n')
  }
  if (value && typeof value === 'object') return pretty(JSON.stringify(value))
  return ''
}

function turnsOf(d: UsageRequestDetail) {
  const turns: { role: 'system' | 'user' | 'assistant'; text: string; final?: boolean }[] = []
  let inbound: { system?: unknown; messages?: { role?: string; content?: unknown }[] } | null = null
  try {
    inbound = JSON.parse(d.inboundBody)
  } catch {
    inbound = null
  }
  if (inbound?.system) turns.push({ role: 'system', text: textOf(inbound.system) })
  for (const m of inbound?.messages ?? []) turns.push({ role: m.role === 'assistant' ? 'assistant' : 'user', text: textOf(m.content) })
  const response = d.responseBody?.trim() ?? ''
  if (response) {
    let parsed: { content?: unknown } | null = null
    try {
      parsed = JSON.parse(response)
    } catch {
      parsed = null
    }
    turns.push({ role: 'assistant', text: parsed?.content ? textOf(parsed.content) : response, final: true })
  }
  return turns
}

function Conversation({ d }: { d: UsageRequestDetail }) {
  const turns = turnsOf(d)
  if (turns.length === 0) return <Empty title="没有解析出对话，到入站里看原文" />
  return (
    <div className="space-y-3">
      {turns.map((t, i) => <Bubble key={i} role={t.role} text={t.text} final={t.final} />)}
    </div>
  )
}

function Bubble({ role, text, final }: { role: 'system' | 'user' | 'assistant'; text: string; final?: boolean }) {
  const long = role === 'system' || text.length > 600
  const [open, setOpen] = useState(!long)
  const label = role === 'system' ? '系统提示' : role === 'user' ? '用户' : final ? '本次响应' : '助手'
  return (
    <div className={cn('flex', role === 'user' ? 'justify-end' : 'justify-start')}>
      <div
        className={cn(
          'max-w-[88%] rounded-xl px-3.5 py-2.5',
          role === 'user' ? 'bg-accent text-white' : role === 'system' ? 'w-full max-w-full bg-fill' : 'bg-fill-strong',
          final && 'ring-1 ring-[var(--accent)]',
        )}
      >
        <button type="button" className={cn('flex w-full items-center justify-between gap-3 text-[12px]', role === 'user' ? 'text-white/75' : 'text-fg2')} onClick={() => setOpen((v) => !v)}>
          <span>{label}</span>
          {long ? <span>{open ? '收起' : `展开 ${text.length.toLocaleString()} 字`}</span> : null}
        </button>
        {open ? <pre className="mt-1 whitespace-pre-wrap break-words font-sans text-[14px] leading-6">{text || '（空）'}</pre> : null}
      </div>
    </div>
  )
}

function Raw({ blocks }: { blocks: [string, string][] }) {
  return (
    <div className="space-y-4">
      {blocks.filter(([, body]) => body).map(([title, body]) => (
        <section key={title}>
          <h3 className="mb-2 text-[13px] font-medium text-fg2">{title}</h3>
          <pre className="max-h-[60vh] overflow-auto rounded-xl bg-fill p-4 font-mono text-[12px] leading-5">{body}</pre>
        </section>
      ))}
    </div>
  )
}

function headerMap(raw: string): Record<string, string> {
  try {
    const v = JSON.parse(raw) as unknown
    if (!v || typeof v !== 'object' || Array.isArray(v)) return {}
    return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, String(x ?? '')]))
  } catch {
    return {}
  }
}

function HeaderDiff({ d }: { d: UsageRequestDetail }) {
  const left = headerMap(d.inboundHeaders)
  const right = headerMap(d.outboundHeaders)
  const keys = [...new Set([...Object.keys(left), ...Object.keys(right)])].sort()
  if (keys.length === 0) return <Empty title="没有记录请求头" />
  return (
    <div className="overflow-x-auto rounded-xl bg-fill">
      <table className="w-full min-w-[560px] text-left text-[12px]">
        <thead className="text-fg2">
          <tr><th className="px-4 py-2.5 font-medium">名称</th><th className="px-4 py-2.5 font-medium">入站</th><th className="px-4 py-2.5 font-medium">出站</th></tr>
        </thead>
        <tbody className="divide-y divide-[var(--line)]">
          {keys.map((k) => (
            <tr key={k} className={cn('align-top', (left[k] ?? '') !== (right[k] ?? '') && 'bg-fill')}>
              <td className="px-4 py-2 font-medium">{k}</td>
              <td className="break-all px-4 py-2 font-mono text-fg2">{left[k] || '—'}</td>
              <td className="break-all px-4 py-2 font-mono">{right[k] || '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
