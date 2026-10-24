import { useState } from 'react'
import { storage } from '@/lib/storage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/field'
import { AppMark } from '@/components/shell'

export function Login({ onLogin }: { onLogin: () => void }) {
  const [key, setKey] = useState(() => storage.getApiKey() ?? '')
  return (
    <div className="flex min-h-screen items-center justify-center p-6">
      <form
        className="glass w-full max-w-[380px] rounded-[20px] p-8 text-center"
        onSubmit={(event) => {
          event.preventDefault()
          if (!key.trim()) return
          storage.setApiKey(key.trim())
          onLogin()
        }}
      >
        <div className="mx-auto mb-5 w-fit scale-[1.6]">
          <AppMark />
        </div>
        <h1 className="mt-8 text-[22px] font-semibold tracking-tight">kiro2api</h1>
        <p className="mt-1 text-[15px] text-fg2">用 Admin Key 登录</p>
        <Input
          className="mt-7 text-center"
          type="password"
          placeholder="Admin Key"
          autoFocus
          value={key}
          onChange={(event) => setKey(event.target.value)}
        />
        <Button type="submit" variant="primary" size="md" className="mt-3 w-full" disabled={!key.trim()}>
          登录
        </Button>
      </form>
    </div>
  )
}
