import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Globe } from 'lucide-react'
import { getGlobalProxy, setGlobalProxy, type GlobalProxy } from '@/api/credentials'
import type { CredentialStatusItem } from '@/types/api'
import { extractErrorMessage } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Field, Input } from '@/components/ui/field'
import { Sheet, SheetBody, SheetHeader } from '@/components/ui/surface'

/** 输入框里回显的代理：带用户名但不带密码，保存时后端沿用原密码 */
function proxyLine(p?: GlobalProxy) {
  if (!p?.proxyUrl) return ''
  if (!p.proxyUsername) return p.proxyUrl
  const [scheme, rest] = p.proxyUrl.split('://')
  return `${scheme}://${p.proxyUsername}@${rest}`
}

function proxyHost(p?: GlobalProxy) {
  return p?.proxyUrl ? p.proxyUrl.replace(/^[a-z0-9]+:\/\//i, '') : ''
}

export function useGlobalProxy() {
  return useQuery({ queryKey: ['global-proxy'], queryFn: getGlobalProxy, staleTime: 60_000 })
}

/** 标题栏的“代理”按钮，点开是设置抽屉 */
export function ProxyButton({ accounts }: { accounts: CredentialStatusItem[] }) {
  const [open, setOpen] = useState(false)
  const proxy = useGlobalProxy()
  return (
    <>
      <Button variant="glass" size="md" onClick={() => setOpen(true)} disabled={proxy.isLoading}>
        <Globe className="h-4 w-4" />
        <span className="max-w-48 truncate">{proxy.data?.proxyUrl ? proxyHost(proxy.data) : '直连'}</span>
      </Button>
      <Sheet open={open} onOpenChange={setOpen}>
        {open ? <ProxyForm current={proxy.data} accounts={accounts} onClose={() => setOpen(false)} /> : null}
      </Sheet>
    </>
  )
}

function ProxyForm({ current, accounts, onClose }: { current?: GlobalProxy; accounts: CredentialStatusItem[]; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [line, setLine] = useState(() => proxyLine(current))
  const [saving, setSaving] = useState(false)
  const own = accounts.filter((a) => a.hasProxy).length
  const follow = accounts.length - own

  const save = async (value: string) => {
    setSaving(true)
    try {
      const next = await setGlobalProxy(value.trim())
      queryClient.setQueryData(['global-proxy'], next)
      queryClient.invalidateQueries({ queryKey: ['credential-balance'] })
      toast.success(next.proxyUrl ? '已设置全局代理' : '已改为直连')
      onClose()
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <SheetHeader title="全局代理" subtitle={`${follow} 个账号跟随全局${own ? `，${own} 个单独设置` : ''}`} />
      <SheetBody>
        <form className="grid gap-4" onSubmit={(event) => { event.preventDefault(); save(line) }}>
          <Field label="代理地址" hint="留空即直连。支持 socks5://user:pass@host:port 或 host:port:user:pass">
            <Input autoFocus autoComplete="off" spellCheck={false} placeholder="socks5://user:pass@host:port" value={line} onChange={(event) => setLine(event.target.value)} />
          </Field>
          <p className="text-[13px] text-fg2">
            批量导入和没填代理的账号都走这里，改完立即生效。账号里单独设置的代理（或 direct）优先。
            {current?.hasPassword ? ' 不改密码时保持输入框里的用户名即可。' : ''}
          </p>
          <div className="flex gap-2">
            <Button type="submit" variant="primary" size="md" disabled={saving}>保存</Button>
            {current?.proxyUrl ? (
              <Button type="button" size="md" disabled={saving} onClick={() => save('')}>改为直连</Button>
            ) : null}
          </div>
        </form>
      </SheetBody>
    </>
  )
}
