import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { formatCNY, formatCostRatio, getAccountCosts, setAccountCost, type AccountCost } from '@/api/usage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { extractErrorMessage } from '@/lib/utils'

function usd(value: number) {
  return `$${value.toFixed(4)}`
}

/**
 * 账号成本和成本倍率，做法对齐 claude2api：
 * 每个账号手填买入的人民币成本，倍率 = 成本 ¥ ÷ 该账号按当前单价折算的累计消耗 $。
 * 不填表示未知，不参与合计；填 0 表示免费账号。
 */
export function AccountCosts() {
  const queryClient = useQueryClient()
  const report = useQuery({ queryKey: ['usage-accounts'], queryFn: getAccountCosts })
  const [drafts, setDrafts] = useState<Record<number, string>>({})
  const [saving, setSaving] = useState<number | null>(null)

  const draftOf = (row: AccountCost) => drafts[row.credentialId] ?? (row.costCny == null ? '' : String(row.costCny))

  const save = async (row: AccountCost) => {
    const raw = draftOf(row).trim()
    const value = raw === '' ? null : Number(raw)
    if (value !== null && (Number.isNaN(value) || value < 0)) {
      toast.error('成本要是非负数字；留空表示未知')
      return
    }
    if (value === row.costCny) {
      setDrafts(({ [row.credentialId]: _, ...rest }) => rest)
      return
    }
    setSaving(row.credentialId)
    try {
      const next = await setAccountCost(row.credentialId, value)
      queryClient.setQueryData(['usage-accounts'], next)
      setDrafts(({ [row.credentialId]: _, ...rest }) => rest)
      toast.success(value === null ? `已清空 ${row.label} 的成本` : `已保存 ${row.label} 的成本`)
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setSaving(null)
    }
  }

  const data = report.data

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-base font-medium">账号成本</h2>
        {data ? (
          <p className="text-sm text-muted-foreground">
            已填 {data.costedAccounts} 个 · 合计 {formatCNY(data.costCny)} · 对应消耗 {usd(data.costedUsageUsd)} · 合计倍率{' '}
            <span className="font-medium text-foreground">{formatCostRatio(data.costRatio)}</span> · 全部消耗 {usd(data.usageUsd)}
          </p>
        ) : null}
      </div>
      <p className="text-sm text-muted-foreground">
        成本填买入这个账号花的人民币，留空表示未知，不参与合计；填 0 表示免费账号。倍率 = 成本 ¥ ÷ 累计消耗 $，累计消耗按上面的当前单价从全部历史 token 重算。
      </p>
      {report.error ? <p className="text-sm text-destructive">{extractErrorMessage(report.error)}</p> : null}
      <div className="overflow-x-auto rounded-md border">
        <table className="w-full min-w-[760px] text-left text-sm">
          <thead className="border-b bg-card text-muted-foreground">
            <tr>
              <th className="px-3 py-2 font-medium">账号</th>
              <th className="px-3 py-2 font-medium">成本 ¥</th>
              <th className="px-3 py-2 font-medium">累计消耗</th>
              <th className="px-3 py-2 font-medium" title="成本 ¥ ÷ 累计消耗 $">倍率</th>
              <th className="px-3 py-2 font-medium">请求</th>
              <th className="px-3 py-2 font-medium">未定价</th>
            </tr>
          </thead>
          <tbody>
            {(data?.accounts ?? []).map((row) => {
              const dirty = row.credentialId in drafts
              return (
                <tr key={row.credentialId} className="border-b last:border-0">
                  <td className="px-3 py-2">
                    {row.label}
                    {row.removed ? <span className="ml-2 text-xs text-muted-foreground">已删除</span> : null}
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex items-center gap-2">
                      <Input
                        className="h-8 w-32"
                        inputMode="decimal"
                        placeholder="未填"
                        aria-label={`${row.label} 的人民币成本`}
                        value={draftOf(row)}
                        onChange={(event) => setDrafts((prev) => ({ ...prev, [row.credentialId]: event.target.value }))}
                        onKeyDown={(event) => {
                          if (event.key === 'Enter') save(row)
                        }}
                      />
                      {dirty ? (
                        <Button size="sm" variant="outline" disabled={saving === row.credentialId} onClick={() => save(row)}>
                          保存
                        </Button>
                      ) : null}
                    </div>
                  </td>
                  <td className="px-3 py-2">{usd(row.usageUsd)}</td>
                  <td className="px-3 py-2 font-medium">{formatCostRatio(row.costRatio)}</td>
                  <td className="px-3 py-2">
                    {row.requests}
                    {row.errors ? <span className="ml-1 text-xs text-muted-foreground">（失败 {row.errors}）</span> : null}
                  </td>
                  <td className="px-3 py-2">{row.unpricedRequests || '—'}</td>
                </tr>
              )
            })}
            {data && data.accounts.length === 0 ? (
              <tr><td className="px-3 py-6 text-muted-foreground" colSpan={6}>还没有账号。</td></tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </section>
  )
}
