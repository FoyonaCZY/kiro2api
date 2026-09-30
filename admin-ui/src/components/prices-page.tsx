import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { deletePrice, getPrices, getUsageSummary, savePrice, type ModelPrice } from '@/api/usage'
import { PageHeader } from '@/components/page-header'
import { AccountCosts } from '@/components/account-costs'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { extractErrorMessage } from '@/lib/utils'

function money(value: number) {
  return `$${value.toFixed(4)}`
}

/** 空字符串表示用默认倍率，返回 null；非法值返回 NaN */
function optionalPrice(value: string): number | null {
  const trimmed = value.trim()
  return trimmed === '' ? null : Number(trimmed)
}

function cacheCell(value: number | null | undefined, input: number, multiplier: number) {
  if (value === null || value === undefined) {
    return <span className="text-muted-foreground">{`${+(input * multiplier).toFixed(6)}（默认 ${multiplier}x）`}</span>
  }
  return value
}

export function PricesPage() {
  const queryClient = useQueryClient()
  const summary = useQuery({ queryKey: ['usage-summary'], queryFn: getUsageSummary })
  const prices = useQuery({ queryKey: ['usage-prices'], queryFn: getPrices })
  const [name, setName] = useState('')
  const [aliases, setAliases] = useState('')
  const [inputPerM, setInputPerM] = useState('')
  const [outputPerM, setOutputPerM] = useState('')
  const [cacheReadPerM, setCacheReadPerM] = useState('')
  const [cacheWrite5mPerM, setCacheWrite5mPerM] = useState('')
  const [cacheWrite1hPerM, setCacheWrite1hPerM] = useState('')
  const [editingId, setEditingId] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['usage-summary'] })
    queryClient.invalidateQueries({ queryKey: ['usage-prices'] })
    queryClient.invalidateQueries({ queryKey: ['usage-requests'] })
    queryClient.invalidateQueries({ queryKey: ['usage-request'] })
    queryClient.invalidateQueries({ queryKey: ['usage-accounts'] })
  }

  const resetForm = () => {
    setEditingId(null)
    setName('')
    setAliases('')
    setInputPerM('')
    setOutputPerM('')
    setCacheReadPerM('')
    setCacheWrite5mPerM('')
    setCacheWrite1hPerM('')
  }

  const edit = (price: ModelPrice) => {
    const text = (value?: number | null) => (value == null ? '' : String(value))
    setEditingId(price.id)
    setName(price.name)
    setAliases(price.aliases.join(', '))
    setInputPerM(String(price.inputPerM))
    setOutputPerM(String(price.outputPerM))
    setCacheReadPerM(text(price.cacheReadPerM))
    setCacheWrite5mPerM(text(price.cacheWrite5mPerM))
    setCacheWrite1hPerM(text(price.cacheWrite1hPerM))
  }

  const submit = async () => {
    const price: ModelPrice = {
      id: editingId ?? '',
      name: name.trim(),
      aliases: aliases.split(',').map((item) => item.trim()).filter(Boolean),
      inputPerM: Number(inputPerM),
      outputPerM: Number(outputPerM),
      cacheReadPerM: optionalPrice(cacheReadPerM),
      cacheWrite5mPerM: optionalPrice(cacheWrite5mPerM),
      cacheWrite1hPerM: optionalPrice(cacheWrite1hPerM),
    }
    if (!price.name || price.aliases.length === 0 || Number.isNaN(price.inputPerM) || Number.isNaN(price.outputPerM)) {
      toast.error('名称、别名和单价都要填')
      return
    }
    const cachePrices = [price.cacheReadPerM, price.cacheWrite5mPerM, price.cacheWrite1hPerM]
    if (cachePrices.some((value) => value !== null && value !== undefined && (Number.isNaN(value) || value < 0))) {
      toast.error('缓存单价要是非负数字，或留空用默认倍率')
      return
    }
    setSaving(true)
    try {
      await savePrice(price)
      resetForm()
      refresh()
      toast.success(editingId ? '已更新定价，历史请求费用已按新单价重算' : '已保存定价，匹配别名的历史请求已按这个单价计费')
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  const remove = async (price: ModelPrice) => {
    try {
      await deletePrice(price.id)
      if (editingId === price.id) resetForm()
      refresh()
    } catch (error) {
      toast.error(extractErrorMessage(error))
    }
  }

  const totals = summary.data

  return (
    <div className="space-y-6">
      <PageHeader
        title="价格"
        description="单价是每百万 token 的美元价格。开启模拟缓存后，缓存读写按缓存单价计费，留空时按输入价的 0.1x / 1.25x / 2x。费用不落库，每次都按当前单价从 token 重算，所以新增或修改单价（含缓存价）后，历史请求、汇总和账号倍率都会立即按新单价更新。未匹配到别名的请求显示为未定价。"
      />

      <div className="grid gap-4 md:grid-cols-5">
        <Card>
          <CardHeader className="pb-2"><CardTitle className="text-sm font-medium text-muted-foreground">请求</CardTitle></CardHeader>
          <CardContent className="text-2xl font-medium">{totals?.requests ?? '—'}</CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2"><CardTitle className="text-sm font-medium text-muted-foreground">失败</CardTitle></CardHeader>
          <CardContent className="text-2xl font-medium">{totals?.errors ?? '—'}</CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2"><CardTitle className="text-sm font-medium text-muted-foreground">输入 / 输出</CardTitle></CardHeader>
          <CardContent className="text-2xl font-medium">{totals ? `${totals.inputTokens} / ${totals.outputTokens}` : '—'}</CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2"><CardTitle className="text-sm font-medium text-muted-foreground">缓存读 / 写</CardTitle></CardHeader>
          <CardContent className="text-2xl font-medium">
            {totals ? `${totals.cacheReadTokens} / ${totals.cacheWrite5mTokens + totals.cacheWrite1hTokens}` : '—'}
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2"><CardTitle className="text-sm font-medium text-muted-foreground">估算费用</CardTitle></CardHeader>
          <CardContent className="text-2xl font-medium">{totals ? money(totals.costUsd) : '—'}</CardContent>
        </Card>
      </div>

      {totals && totals.unpricedRequests > 0 ? (
        <p className="text-sm text-muted-foreground">{totals.unpricedRequests} 条请求没有匹配到单价，不计入费用。</p>
      ) : null}

      <div className="grid gap-3 md:grid-cols-4">
        <Input placeholder="名称，例如 Opus" value={name} onChange={(event) => setName(event.target.value)} />
        <Input placeholder="别名，逗号分隔" value={aliases} onChange={(event) => setAliases(event.target.value)} />
        <Input placeholder="输入 $/1M" value={inputPerM} onChange={(event) => setInputPerM(event.target.value)} />
        <Input placeholder="输出 $/1M" value={outputPerM} onChange={(event) => setOutputPerM(event.target.value)} />
        <Input placeholder="缓存读 $/1M（留空 0.1x）" value={cacheReadPerM} onChange={(event) => setCacheReadPerM(event.target.value)} />
        <Input placeholder="5 分钟缓存写 $/1M（留空 1.25x）" value={cacheWrite5mPerM} onChange={(event) => setCacheWrite5mPerM(event.target.value)} />
        <Input placeholder="1 小时缓存写 $/1M（留空 2x）" value={cacheWrite1hPerM} onChange={(event) => setCacheWrite1hPerM(event.target.value)} />
        <div className="flex gap-2">
          <Button className="flex-1" onClick={submit} disabled={saving}>{editingId ? '更新' : '保存'}</Button>
          {editingId ? <Button variant="outline" onClick={resetForm} disabled={saving}>取消</Button> : null}
        </div>
      </div>

      <div className="overflow-x-auto rounded-md border">
        <table className="w-full text-left text-sm">
          <thead className="border-b bg-card text-muted-foreground">
            <tr>
              <th className="px-3 py-2 font-medium">名称</th>
              <th className="px-3 py-2 font-medium">别名</th>
              <th className="px-3 py-2 font-medium">输入</th>
              <th className="px-3 py-2 font-medium">输出</th>
              <th className="px-3 py-2 font-medium">缓存读</th>
              <th className="px-3 py-2 font-medium">5 分钟写</th>
              <th className="px-3 py-2 font-medium">1 小时写</th>
              <th className="px-3 py-2 font-medium"></th>
            </tr>
          </thead>
          <tbody>
            {(prices.data ?? []).map((price) => (
              <tr key={price.id} className="border-b last:border-0">
                <td className="px-3 py-2">{price.name}</td>
                <td className="px-3 py-2">{price.aliases.join(', ')}</td>
                <td className="px-3 py-2">{price.inputPerM}</td>
                <td className="px-3 py-2">{price.outputPerM}</td>
                <td className="px-3 py-2">{cacheCell(price.cacheReadPerM, price.inputPerM, 0.1)}</td>
                <td className="px-3 py-2">{cacheCell(price.cacheWrite5mPerM, price.inputPerM, 1.25)}</td>
                <td className="px-3 py-2">{cacheCell(price.cacheWrite1hPerM, price.inputPerM, 2)}</td>
                <td className="px-3 py-2 text-right">
                  <Button variant="ghost" size="sm" onClick={() => edit(price)}>编辑</Button>
                  <Button variant="ghost" size="sm" onClick={() => remove(price)}>删除</Button>
                </td>
              </tr>
            ))}
            {prices.data && prices.data.length === 0 ? (
              <tr><td className="px-3 py-6 text-muted-foreground" colSpan={8}>还没有定价。</td></tr>
            ) : null}
          </tbody>
        </table>
      </div>

      {totals && totals.byModel.length > 0 ? (
        <div className="overflow-x-auto rounded-md border">
          <table className="w-full text-left text-sm">
            <thead className="border-b bg-card text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">模型</th>
                <th className="px-3 py-2 font-medium">请求</th>
                <th className="px-3 py-2 font-medium">失败</th>
                <th className="px-3 py-2 font-medium">输入</th>
                <th className="px-3 py-2 font-medium">输出</th>
                <th className="px-3 py-2 font-medium">缓存读</th>
                <th className="px-3 py-2 font-medium">缓存写</th>
                <th className="px-3 py-2 font-medium">费用</th>
              </tr>
            </thead>
            <tbody>
              {totals.byModel.map((row) => (
                <tr key={row.model} className="border-b last:border-0">
                  <td className="px-3 py-2">{row.model}</td>
                  <td className="px-3 py-2">{row.requests}</td>
                  <td className="px-3 py-2">{row.errors}</td>
                  <td className="px-3 py-2">{row.inputTokens}</td>
                  <td className="px-3 py-2">{row.outputTokens}</td>
                  <td className="px-3 py-2">{row.cacheReadTokens}</td>
                  <td className="px-3 py-2">{row.cacheWrite5mTokens + row.cacheWrite1hTokens}</td>
                  <td className="px-3 py-2">{money(row.costUsd)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      <AccountCosts />
    </div>
  )
}
