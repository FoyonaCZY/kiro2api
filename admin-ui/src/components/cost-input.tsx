import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { setAccountCost } from '@/api/usage'
import { extractErrorMessage } from '@/lib/utils'

/** 账号的人民币成本，失焦或回车保存；留空表示未知，0 表示免费 */
export function CostInput({ credentialId, value, align = 'right' }: { credentialId: number; value: number | null; align?: 'left' | 'right' }) {
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState(value == null ? '' : String(value))
  useEffect(() => setDraft(value == null ? '' : String(value)), [value])

  const save = async () => {
    const raw = draft.trim()
    const next = raw === '' ? null : Number(raw)
    if (next === value) return
    if (next !== null && (Number.isNaN(next) || next < 0)) {
      toast.error('成本填非负数字，留空表示未知')
      setDraft(value == null ? '' : String(value))
      return
    }
    try {
      const report = await setAccountCost(credentialId, next)
      queryClient.setQueryData(['usage-accounts'], report)
    } catch (error) {
      toast.error(extractErrorMessage(error))
    }
  }

  return (
    <span className="inline-flex items-center gap-1 text-fg2">
      ¥
      <input
        inputMode="decimal"
        aria-label="人民币成本"
        placeholder="未填"
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={save}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          if (event.key === 'Enter') event.currentTarget.blur()
        }}
        className={`tnum w-16 rounded-lg bg-transparent px-1.5 py-0.5 ${align === 'right' ? 'text-right' : 'text-left'} text-fg outline-none transition placeholder:text-fg3 hover:bg-fill focus:bg-fill-strong`}
      />
    </span>
  )
}
