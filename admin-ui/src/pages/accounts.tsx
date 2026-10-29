import { useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Plus, Search, X } from 'lucide-react'
import { deleteCredential, forceRefreshToken, getCredentialBalance, resetCredentialFailure, setCredentialDisabled } from '@/api/credentials'
import { formatCredits, getAccountCosts } from '@/api/usage'
import { useCredentials, useLoadBalancingMode, useSetDisabled, useSetLoadBalancingMode } from '@/hooks/use-credentials'
import type { CredentialStatusItem } from '@/types/api'
import { accountName, accountState, methodName, relTime, stateText, stateTone, usd, type AccountState } from '@/lib/format'
import { cn, extractErrorMessage } from '@/lib/utils'
import { PageTitle } from '@/components/shell'
import { Button, ConfirmButton } from '@/components/ui/button'
import { Badge, Checkbox, Dot, Segmented, Spinner, Switch } from '@/components/ui/controls'
import { Empty, EmptyRow, MenuItem, MenuSeparator, Panel, RowMenu, Table, Td, Th, THead, Tr } from '@/components/ui/surface'
import { AccountSheet } from '@/components/account-sheet'
import { AddAccountSheet } from '@/components/add-account'
import { ProxyButton } from '@/components/proxy-sheet'

type Filter = 'all' | 'ok' | 'issue' | 'off'

const inFilter = (filter: Filter, state: AccountState) =>
  filter === 'all' || (filter === 'ok' && state === 'ok') || (filter === 'issue' && (state === 'cooling' || state === 'failing')) || (filter === 'off' && state === 'off')

interface Probe {
  status: 'running' | 'ok' | 'failed'
  text?: string
}

interface Job {
  label: string
  done: number
  total: number
}

export function AccountsPage() {
  const queryClient = useQueryClient()
  const { data, isLoading, error, refetch } = useCredentials()
  const costs = useQuery({ queryKey: ['usage-accounts'], queryFn: getAccountCosts })
  const mode = useLoadBalancingMode()
  const setMode = useSetLoadBalancingMode()
  const setDisabled = useSetDisabled()
  const [filter, setFilter] = useState<Filter>('all')
  const [query, setQuery] = useState('')
  const [openId, setOpenId] = useState<number | null>(null)
  const [adding, setAdding] = useState(false)
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [probes, setProbes] = useState<Map<number, Probe>>(new Map())
  const [job, setJob] = useState<Job | null>(null)
  const cancel = useRef(false)

  const all = data?.credentials ?? []
  const costOf = useMemo(() => new Map((costs.data?.accounts ?? []).map((a) => [a.credentialId, a])), [costs.data])
  const counts = useMemo(() => {
    const c = { all: all.length, ok: 0, issue: 0, off: 0 }
    for (const item of all) {
      const s = accountState(item)
      if (s === 'ok') c.ok++
      else if (s === 'off') c.off++
      else c.issue++
    }
    return c
  }, [all])
  const q = query.trim().toLowerCase()
  const rows = all.filter((c) => inFilter(filter, accountState(c)) && (!q || `${c.email ?? ''} #${c.id} ${c.proxyUrl ?? ''}`.toLowerCase().includes(q)))
  const open = all.find((c) => c.id === openId) ?? null
  const allSelected = rows.length > 0 && rows.every((c) => selected.has(c.id))

  const toggle = (id: number) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  /** 依次处理，进度显示在表格上方的工具栏里 */
  const runEach = async (label: string, ids: number[], fn: (id: number) => Promise<unknown>, gapMs = 0) => {
    cancel.current = false
    setJob({ label, done: 0, total: ids.length })
    let ok = 0
    let failed = 0
    for (let i = 0; i < ids.length; i++) {
      if (cancel.current) break
      try {
        await fn(ids[i])
        ok++
      } catch {
        failed++
      }
      setJob({ label, done: i + 1, total: ids.length })
      if (gapMs && i < ids.length - 1 && !cancel.current) await new Promise((r) => setTimeout(r, gapMs))
    }
    setJob(null)
    await queryClient.invalidateQueries({ queryKey: ['credentials'] })
    const summary = failed ? `${label}：${ok} 个成功，${failed} 个失败` : `${label}：${ok} 个完成`
    if (failed) toast.warning(summary)
    else toast.success(summary)
  }

  const probe = async (id: number) => {
    setProbes((prev) => new Map(prev).set(id, { status: 'running' }))
    try {
      const b = await getCredentialBalance(id)
      setProbes((prev) => new Map(prev).set(id, { status: 'ok', text: `额度 ${+b.currentUsage.toFixed(2)}/${b.usageLimit}` }))
    } catch (err) {
      setProbes((prev) => new Map(prev).set(id, { status: 'failed', text: extractErrorMessage(err) }))
      throw err
    }
  }
  const remove = async (targets: number[]) => {
    await runEach('删除', targets, async (id) => {
      if (!all.find((c) => c.id === id)?.disabled) await setCredentialDisabled(id, true)
      await deleteCredential(id)
    })
    setSelected(new Set())
  }
  const single = (label: string, fn: () => Promise<unknown>) =>
    fn()
      .then(() => {
        queryClient.invalidateQueries({ queryKey: ['credentials'] })
        toast.success(label)
      })
      .catch((err) => toast.error(extractErrorMessage(err)))
  const askDelete = (c: CredentialStatusItem) =>
    toast(`删除 ${accountName(c)}？`, {
      description: '删除后无法恢复',
      action: { label: '删除', onClick: () => remove([c.id]) },
      cancel: { label: '取消', onClick: () => undefined },
    })

  if (isLoading) {
    return <div className="flex justify-center py-32"><Spinner className="h-6 w-6" /></div>
  }
  if (error) {
    return (
      <Empty title={`读取账号失败：${extractErrorMessage(error)}`}>
        <Button onClick={() => refetch()}>重试</Button>
      </Empty>
    )
  }

  const ids = [...selected]

  return (
    <>
      <PageTitle title="账号" subtitle={`${counts.ok} 个可用，共 ${counts.all} 个`}>
        <span className="text-[13px] text-fg2">调度</span>
        <Segmented
          value={mode.data?.mode ?? 'priority'}
          disabled={mode.isLoading || setMode.isPending}
          onChange={(next) => setMode.mutate(next, { onError: (err) => toast.error(extractErrorMessage(err)) })}
          options={[
            { value: 'priority', label: '按优先级' },
            { value: 'balanced', label: '近1分钟' },
          ]}
        />
        <ProxyButton accounts={all} />
        <Button variant="primary" size="md" onClick={() => setAdding(true)}>
          <Plus className="h-4 w-4" />
          添加账号
        </Button>
      </PageTitle>

      {all.length === 0 ? (
        <Panel>
          <Empty title="还没有账号">
            <Button variant="primary" onClick={() => setAdding(true)}>添加第一个账号</Button>
          </Empty>
        </Panel>
      ) : (
        <>
          <div className="mb-3 flex min-h-9 flex-wrap items-center gap-2">
            {job ? (
              <>
                <Spinner />
                <span className="tnum text-[13px] font-medium">{job.label}中 {job.done}/{job.total}</span>
                <Button size="sm" onClick={() => { cancel.current = true }}>停止</Button>
              </>
            ) : selected.size > 0 ? (
              <>
                <span className="tnum mr-1 text-[13px] font-medium">已选 {selected.size} 个</span>
                <Button onClick={() => runEach('验活', ids, probe, 2000)}>验活</Button>
                <Button onClick={() => runEach('刷新令牌', ids.filter((id) => !all.find((c) => c.id === id)?.disabled), forceRefreshToken)}>刷新令牌</Button>
                <Button onClick={() => runEach('清零失败', ids, resetCredentialFailure)}>清零失败</Button>
                <ConfirmButton confirmText={`删除 ${selected.size} 个`} onConfirm={() => remove(ids)}>删除</ConfirmButton>
                <Button variant="plain" onClick={() => setSelected(new Set())}>取消选择</Button>
              </>
            ) : (
              <>
                <Segmented
                  value={filter}
                  onChange={setFilter}
                  options={[
                    { value: 'all', label: `全部 ${counts.all}` },
                    { value: 'ok', label: `可用 ${counts.ok}` },
                    { value: 'issue', label: `异常 ${counts.issue}` },
                    { value: 'off', label: `停用 ${counts.off}` },
                  ]}
                />
                {filter === 'off' && counts.off > 0 ? (
                  <ConfirmButton confirmText={`删除 ${counts.off} 个`} onConfirm={() => remove(all.filter((c) => c.disabled).map((c) => c.id))}>
                    全部删除
                  </ConfirmButton>
                ) : null}
              </>
            )}
            <label className="ml-auto flex h-8 w-full items-center gap-2 rounded-[8px] border border-[var(--line)] bg-fill px-2.5 sm:w-64">
              <Search className="h-3.5 w-3.5 shrink-0 text-fg3" />
              <input className="min-w-0 flex-1 bg-transparent text-[13px] outline-none placeholder:text-fg3" placeholder="搜索邮箱、编号、代理" value={query} onChange={(e) => setQuery(e.target.value)} />
              {query ? <button type="button" aria-label="清空" onClick={() => setQuery('')}><X className="h-3.5 w-3.5 text-fg3" /></button> : null}
            </label>
          </div>

          <Table minWidth={860}>
            <THead>
              <Th className="w-10">
                <Checkbox
                  label="全选"
                  checked={allSelected}
                  indeterminate={!allSelected && rows.some((c) => selected.has(c.id))}
                  onChange={() => setSelected(allSelected ? new Set() : new Set(rows.map((c) => c.id)))}
                />
              </Th>
              <Th>账号</Th>
              <Th>状态</Th>
              <Th className="hidden lg:table-cell">类型</Th>
              <Th className="text-right">优先级</Th>
              <Th className="text-right">成功请求</Th>
              <Th>最近使用</Th>
              <Th className="hidden text-right lg:table-cell">消耗</Th>
              <Th className="hidden text-right xl:table-cell">Credits</Th>
              <Th className="w-14">启用</Th>
              <Th className="w-10" />
            </THead>
            <tbody>
              {rows.length === 0 ? <EmptyRow colSpan={11}>没有符合条件的账号</EmptyRow> : null}
              {rows.map((c) => (
                <AccountRow
                  key={c.id}
                  c={c}
                  selected={selected.has(c.id)}
                  probe={probes.get(c.id)}
                  usage={costOf.get(c.id)}
                  onSelect={() => toggle(c.id)}
                  onOpen={() => setOpenId(c.id)}
                  onToggle={(on) => setDisabled.mutate({ id: c.id, disabled: !on }, { onError: (err) => toast.error(extractErrorMessage(err)) })}
                  onCheck={() => probe(c.id).catch(() => undefined)}
                  onRefresh={() => single('已刷新令牌', () => forceRefreshToken(c.id))}
                  onReset={() => single('已清零失败次数', () => resetCredentialFailure(c.id))}
                  onDelete={() => askDelete(c)}
                />
              ))}
            </tbody>
          </Table>
        </>
      )}

      <AccountSheet credential={open} onClose={() => setOpenId(null)} />
      <AddAccountSheet open={adding} onOpenChange={setAdding} existing={all} />
    </>
  )
}

function AccountRow({
  c,
  selected,
  probe,
  usage,
  onSelect,
  onOpen,
  onToggle,
  onCheck,
  onRefresh,
  onReset,
  onDelete,
}: {
  c: CredentialStatusItem
  selected: boolean
  probe?: Probe
  usage?: { usageUsd: number; credits: number }
  onSelect: () => void
  onOpen: () => void
  onToggle: (on: boolean) => void
  onCheck: () => void
  onRefresh: () => void
  onReset: () => void
  onDelete: () => void
}) {
  const state = accountState(c)
  const oauth = c.authMethod !== 'api_key' && c.authMethod !== 'claude_api'
  return (
    <Tr onClick={onOpen} selected={selected}>
      <Td>
        <Checkbox checked={selected} onChange={onSelect} label="选择" />
      </Td>
      <Td className="max-w-[280px]">
        <div className="flex items-center gap-2">
          <span className={cn('truncate font-medium', state === 'off' && 'text-fg2')}>{accountName(c)}</span>
          {c.isCurrent ? <Badge tone="accent">当前</Badge> : null}
        </div>
      </Td>
      <Td className="max-w-[260px]">
        {probe ? (
          <span className={cn('flex items-center gap-2 truncate', probe.status === 'failed' ? 'text-bad' : probe.status === 'ok' ? 'text-ok' : 'text-fg2')}>
            {probe.status === 'running' ? <Spinner className="h-3 w-3" /> : <Dot tone={probe.status === 'ok' ? 'ok' : 'bad'} />}
            <span className="truncate">{probe.status === 'running' ? '验活中' : probe.text}</span>
          </span>
        ) : (
          <span className={cn('flex items-center gap-2 truncate', state === 'ok' || state === 'off' ? 'text-fg2' : state === 'cooling' ? 'text-warn' : 'text-bad')}>
            <Dot tone={stateTone[state]} />
            <span className="truncate">{stateText(c)}</span>
          </span>
        )}
      </Td>
      <Td className="hidden text-fg2 lg:table-cell">{methodName(c.authMethod)}</Td>
      <Td className="tnum text-right text-fg2">{c.priority}</Td>
      <Td className="tnum text-right">{c.successCount.toLocaleString()}</Td>
      <Td className="text-fg2">{relTime(c.lastUsedAt)}</Td>
      <Td className="tnum hidden text-right lg:table-cell">{usage ? usd(usage.usageUsd) : '—'}</Td>
      <Td className="tnum hidden text-right text-fg2 xl:table-cell">{usage ? formatCredits(usage.credits) : '—'}</Td>
      <Td>
        <Switch label="启用" checked={!c.disabled} onCheckedChange={onToggle} />
      </Td>
      <Td>
        <RowMenu>
          <MenuItem onSelect={onOpen}>查看详情</MenuItem>
          {c.authMethod !== 'claude_api' ? <MenuItem disabled={c.disabled} onSelect={onCheck}>验活</MenuItem> : null}
          {oauth ? <MenuItem disabled={c.disabled} onSelect={onRefresh}>刷新令牌</MenuItem> : null}
          {state === 'failing' ? <MenuItem onSelect={onReset}>清零失败次数</MenuItem> : null}
          <MenuSeparator />
          <MenuItem danger onSelect={onDelete}>删除</MenuItem>
        </RowMenu>
      </Td>
    </Tr>
  )
}
