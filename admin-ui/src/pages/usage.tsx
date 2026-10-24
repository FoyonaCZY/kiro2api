import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Plus } from 'lucide-react'
import { deletePrice, formatCNY, formatCostRatio, formatCredits, getAccountCosts, getPrices, getUsageSummary, savePrice, type ModelPrice } from '@/api/usage'
import { compact, usd } from '@/lib/format'
import { extractErrorMessage } from '@/lib/utils'
import { PageTitle } from '@/components/shell'
import { Button, ConfirmButton } from '@/components/ui/button'
import { Badge, Spinner } from '@/components/ui/controls'
import { Field, Input } from '@/components/ui/field'
import { Empty, EmptyRow, Panel, SectionHeader, Sheet, SheetBody, SheetHeader, Table, Td, Th, THead, Tr } from '@/components/ui/surface'
import { CostInput } from '@/components/cost-input'

export function UsagePage() {
  const summary = useQuery({ queryKey: ['usage-summary'], queryFn: getUsageSummary })
  const accounts = useQuery({ queryKey: ['usage-accounts'], queryFn: getAccountCosts })
  const prices = useQuery({ queryKey: ['usage-prices'], queryFn: getPrices })
  const [editing, setEditing] = useState<ModelPrice | 'new' | null>(null)
  const s = summary.data
  const rep = accounts.data

  if (summary.isLoading) return <div className="flex justify-center py-32"><Spinner className="h-6 w-6" /></div>
  if (summary.error) return <Empty title={`读取失败：${extractErrorMessage(summary.error)}`} />

  return (
    <div className="space-y-8">
      <div>
        <PageTitle title="用量" />
        {s ? (
          <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
            <Metric label="费用" value={usd(s.costUsd)} note={s.unpricedRequests ? `${s.unpricedRequests} 条请求没有单价` : '按当前单价计算'} />
            <Metric label="Credits" value={formatCredits(s.credits)} note="上游实际扣除" />
            <Metric label="请求" value={s.requests.toLocaleString()} note={s.errors ? `失败 ${s.errors.toLocaleString()} 次` : '全部成功'} />
            <Metric label="输入 / 输出 Token" value={`${compact(s.inputTokens)} / ${compact(s.outputTokens)}`} note={`缓存读 ${compact(s.cacheReadTokens)}`} />
          </div>
        ) : null}
      </div>

      <div className="grid gap-8 2xl:grid-cols-2">
        <section>
          <SectionHeader title="按模型" />
          <Table minWidth={560}>
            <THead>
              <Th>模型</Th>
              <Th className="text-right">请求</Th>
              <Th className="text-right">失败</Th>
              <Th className="text-right">输入</Th>
              <Th className="text-right">输出</Th>
              <Th className="text-right">费用</Th>
              <Th className="text-right">Credits</Th>
            </THead>
            <tbody>
              {(s?.byModel ?? []).length === 0 ? <EmptyRow colSpan={7}>还没有用量</EmptyRow> : null}
              {(s?.byModel ?? []).map((m) => (
                <Tr key={m.model}>
                  <Td className="font-medium">{m.model}</Td>
                  <Td className="tnum text-right">{m.requests.toLocaleString()}</Td>
                  <Td className={m.errors ? 'tnum text-right text-bad' : 'tnum text-right text-fg3'}>{m.errors.toLocaleString()}</Td>
                  <Td className="tnum text-right text-fg2">{compact(m.inputTokens)}</Td>
                  <Td className="tnum text-right text-fg2">{compact(m.outputTokens)}</Td>
                  <Td className="tnum text-right font-medium">{usd(m.costUsd)}</Td>
                  <Td className="tnum text-right text-fg2">{formatCredits(m.credits)}</Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        </section>

        <section>
          <SectionHeader title="单价">
            <Button onClick={() => setEditing('new')}>
              <Plus className="h-3.5 w-3.5" />
              添加单价
            </Button>
          </SectionHeader>
          <Table minWidth={520}>
            <THead>
              <Th>名称</Th>
              <Th>匹配的模型</Th>
              <Th className="text-right">输入</Th>
              <Th className="text-right">输出</Th>
              <Th className="text-right">缓存读</Th>
            </THead>
            <tbody>
              {(prices.data ?? []).length === 0 ? <EmptyRow colSpan={5}>还没有单价，费用都会显示为未定价</EmptyRow> : null}
              {(prices.data ?? []).map((p) => (
                <Tr key={p.id} onClick={() => setEditing(p)}>
                  <Td className="font-medium">{p.name}</Td>
                  <Td className="max-w-[260px] truncate text-fg2">{p.aliases.join('，')}</Td>
                  <Td className="tnum text-right">${p.inputPerM}</Td>
                  <Td className="tnum text-right">${p.outputPerM}</Td>
                  <Td className="tnum text-right text-fg2">{p.cacheReadPerM != null ? `$${p.cacheReadPerM}` : '默认'}</Td>
                </Tr>
              ))}
            </tbody>
          </Table>
          <p className="mt-2 text-[12px] text-fg3">美元每百万 token。改价后历史费用按新价重算。</p>
        </section>
      </div>

      {rep && rep.accounts.length > 0 ? (
        <section>
          <SectionHeader title="账号成本">
            {rep.costRatio != null ? (
              <span className="tnum text-[13px] text-fg2">
                已填 {rep.costedAccounts} 个，{formatCNY(rep.costCny)} 换 {usd(rep.costedUsageUsd)}，合计倍率 <span className="font-semibold text-fg">{formatCostRatio(rep.costRatio)}</span>
              </span>
            ) : null}
          </SectionHeader>
          <Table minWidth={760}>
            <THead>
              <Th>账号</Th>
              <Th className="text-right">请求</Th>
              <Th className="text-right">消耗</Th>
              <Th className="text-right">Credits</Th>
              <Th className="text-right">成本</Th>
              <Th className="text-right">倍率</Th>
              <Th className="text-right">每 credit</Th>
            </THead>
            <tbody>
              {rep.accounts.map((a) => (
                <Tr key={a.credentialId}>
                  <Td className="max-w-[300px] truncate">
                    <span className="font-medium">{a.label}</span>
                    {a.removed ? <Badge className="ml-2">已删除</Badge> : null}
                  </Td>
                  <Td className="tnum text-right text-fg2">{a.requests.toLocaleString()}</Td>
                  <Td className="tnum text-right">{usd(a.usageUsd)}</Td>
                  <Td className="tnum text-right text-fg2">{formatCredits(a.credits)}</Td>
                  <Td className="text-right"><CostInput credentialId={a.credentialId} value={a.costCny} /></Td>
                  <Td className="tnum text-right font-medium">{formatCostRatio(a.costRatio)}</Td>
                  <Td className="tnum text-right text-fg2">{a.costCny != null && a.credits > 0 ? formatCNY(a.costCny / a.credits) : '—'}</Td>
                </Tr>
              ))}
            </tbody>
          </Table>
          <p className="mt-2 text-[12px] text-fg3">成本填买入账号花的人民币，留空表示未知，填 0 表示免费。倍率 = 成本 ÷ 消耗。</p>
        </section>
      ) : null}

      <PriceSheet price={editing} onClose={() => setEditing(null)} />
    </div>
  )
}

function Metric({ label, value, note }: { label: string; value: string; note: string }) {
  return (
    <Panel className="px-5 py-4">
      <div className="text-[13px] text-fg2">{label}</div>
      <div className="tnum mt-1.5 truncate text-[26px] font-semibold tracking-tight">{value}</div>
      <div className="mt-1 truncate text-[12px] text-fg3">{note}</div>
    </Panel>
  )
}

function PriceSheet({ price, onClose }: { price: ModelPrice | 'new' | null; onClose: () => void }) {
  return (
    <Sheet open={price != null} onOpenChange={(open) => { if (!open) onClose() }}>
      {price ? <PriceForm key={price === 'new' ? 'new' : price.id} price={price === 'new' ? null : price} onClose={onClose} /> : null}
    </Sheet>
  )
}

function PriceForm({ price, onClose }: { price: ModelPrice | null; onClose: () => void }) {
  const queryClient = useQueryClient()
  const text = (v?: number | null) => (v == null ? '' : String(v))
  const [f, setF] = useState({
    name: price?.name ?? '',
    aliases: price?.aliases.join(', ') ?? '',
    input: text(price?.inputPerM),
    output: text(price?.outputPerM),
    cacheRead: text(price?.cacheReadPerM),
    write5m: text(price?.cacheWrite5mPerM),
    write1h: text(price?.cacheWrite1hPerM),
  })
  const [saving, setSaving] = useState(false)
  const set = (key: keyof typeof f) => (event: React.ChangeEvent<HTMLInputElement>) => setF((prev) => ({ ...prev, [key]: event.target.value }))
  const opt = (s: string) => (s.trim() === '' ? null : Number(s))
  const input = Number(f.input)

  const refresh = () => {
    for (const key of ['usage-summary', 'usage-prices', 'usage-requests', 'usage-request', 'usage-accounts']) {
      queryClient.invalidateQueries({ queryKey: [key] })
    }
  }

  const save = async () => {
    const body: ModelPrice = {
      id: price?.id ?? '',
      name: f.name.trim(),
      aliases: f.aliases.split(/[,，\s]+/).map((a) => a.trim()).filter(Boolean),
      inputPerM: Number(f.input),
      outputPerM: Number(f.output),
      cacheReadPerM: opt(f.cacheRead),
      cacheWrite5mPerM: opt(f.write5m),
      cacheWrite1hPerM: opt(f.write1h),
    }
    const nums = [body.inputPerM, body.outputPerM, body.cacheReadPerM, body.cacheWrite5mPerM, body.cacheWrite1hPerM]
    if (!body.name || body.aliases.length === 0 || f.input.trim() === '' || f.output.trim() === '' || nums.some((n) => n != null && (Number.isNaN(n) || n < 0))) {
      toast.error('名称、模型名和输入输出单价要填，单价不能为负')
      return
    }
    setSaving(true)
    try {
      await savePrice(body)
      refresh()
      toast.success('已保存，历史费用已按新单价重算')
      onClose()
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    if (!price) return
    try {
      await deletePrice(price.id)
      refresh()
      toast.success('已删除')
      onClose()
    } catch (error) {
      toast.error(extractErrorMessage(error))
    }
  }

  const ph = (m: number) => (Number.isFinite(input) && f.input.trim() ? `默认 ${+(input * m).toFixed(4)}` : `默认输入价 ×${m}`)

  return (
    <>
      <SheetHeader title={price ? '编辑单价' : '添加单价'} subtitle="美元，每百万 token" />
      <SheetBody>
        <form className="grid gap-4" onSubmit={(event) => { event.preventDefault(); save() }}>
          <Field label="名称"><Input placeholder="Opus" value={f.name} onChange={set('name')} autoFocus={!price} /></Field>
          <Field label="模型名" hint="请求里的 model，多个用逗号分开，不区分大小写">
            <Input placeholder="claude-opus-4-6, claude-opus-4-6-thinking" value={f.aliases} onChange={set('aliases')} />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="输入"><Input inputMode="decimal" value={f.input} onChange={set('input')} /></Field>
            <Field label="输出"><Input inputMode="decimal" value={f.output} onChange={set('output')} /></Field>
          </div>
          <div className="grid grid-cols-3 gap-3">
            <Field label="缓存读"><Input inputMode="decimal" placeholder={ph(0.1)} value={f.cacheRead} onChange={set('cacheRead')} /></Field>
            <Field label="5 分钟写"><Input inputMode="decimal" placeholder={ph(1.25)} value={f.write5m} onChange={set('write5m')} /></Field>
            <Field label="1 小时写"><Input inputMode="decimal" placeholder={ph(2)} value={f.write1h} onChange={set('write1h')} /></Field>
          </div>
          <div className="mt-2 flex gap-2">
            {price ? <ConfirmButton size="md" onConfirm={remove}>删除</ConfirmButton> : null}
            <Button type="submit" variant="primary" size="md" className="flex-1" disabled={saving}>保存</Button>
          </div>
        </form>
      </SheetBody>
    </>
  )
}
