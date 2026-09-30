import { useState } from 'react'
import { toast } from 'sonner'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { completeSocialLogin, startSocialLogin } from '@/api/credentials'
import { extractErrorMessage } from '@/lib/utils'
import { useQueryClient } from '@tanstack/react-query'

interface SocialLoginDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function SocialLoginDialog({ open, onOpenChange }: SocialLoginDialogProps) {
  const queryClient = useQueryClient()
  const [proxyUrl, setProxyUrl] = useState('')
  const [proxyUsername, setProxyUsername] = useState('')
  const [proxyPassword, setProxyPassword] = useState('')
  const [authorizationUrl, setAuthorizationUrl] = useState('')
  const [callbackUrl, setCallbackUrl] = useState('')
  const [starting, setStarting] = useState(false)
  const [completing, setCompleting] = useState(false)

  const reset = () => {
    setProxyUrl('')
    setProxyUsername('')
    setProxyPassword('')
    setAuthorizationUrl('')
    setCallbackUrl('')
  }

  const handleStart = async () => {
    setStarting(true)
    try {
      const result = await startSocialLogin({
        proxyUrl: proxyUrl.trim(),
        proxyUsername: proxyUsername.trim() || undefined,
        proxyPassword: proxyPassword.trim() || undefined,
      })
      setAuthorizationUrl(result.authorizationUrl)
      toast.success('授权链接已生成')
    } catch (error) {
      toast.error(`生成失败: ${extractErrorMessage(error)}`)
    } finally {
      setStarting(false)
    }
  }

  const handleComplete = async () => {
    setCompleting(true)
    try {
      const result = await completeSocialLogin(callbackUrl.trim())
      toast.success(result.message)
      await queryClient.invalidateQueries({ queryKey: ['credentials'] })
      onOpenChange(false)
      reset()
    } catch (error) {
      toast.error(`添加失败: ${extractErrorMessage(error)}`)
    } finally {
      setCompleting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg max-h-[85vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>SOCKS5 登录添加账号</DialogTitle>
        </DialogHeader>
        <div className="space-y-4 py-4 overflow-y-auto flex-1 pr-1">
          <div className="space-y-2">
            <label className="text-sm font-medium">SOCKS5 代理</label>
            <Input
              placeholder="socks5://主机:端口 或 socks5h://"
              value={proxyUrl}
              onChange={(e) => setProxyUrl(e.target.value)}
              disabled={starting || completing}
            />
            <div className="grid grid-cols-2 gap-2">
              <Input
                placeholder="代理用户名"
                value={proxyUsername}
                onChange={(e) => setProxyUsername(e.target.value)}
                disabled={starting || completing}
              />
              <Input
                type="password"
                placeholder="代理密码"
                value={proxyPassword}
                onChange={(e) => setProxyPassword(e.target.value)}
                disabled={starting || completing}
              />
            </div>
            <Button type="button" onClick={handleStart} disabled={starting || !proxyUrl.trim()}>
              {starting ? '生成中...' : '生成授权链接'}
            </Button>
          </div>
          {authorizationUrl && (
            <div className="space-y-2">
              <label className="text-sm font-medium">授权链接</label>
              <textarea
                readOnly
                value={authorizationUrl}
                className="w-full min-h-24 rounded-md border bg-muted px-3 py-2 text-xs"
              />
              <p className="text-xs text-muted-foreground">
                用已经挂上这条 SOCKS5 的浏览器打开。登录后地址栏会变成
                http://localhost:3128/oauth/callback?... 页面打不开也没关系，把整段地址复制回来。
              </p>
              <Button
                type="button"
                variant="outline"
                onClick={() => navigator.clipboard.writeText(authorizationUrl)}
              >
                复制链接
              </Button>
            </div>
          )}
          <div className="space-y-2">
            <label className="text-sm font-medium">回调 URL</label>
            <textarea
              value={callbackUrl}
              onChange={(e) => setCallbackUrl(e.target.value)}
              placeholder="http://localhost:3128/oauth/callback?code=...&state=...&login_option=github"
              className="w-full min-h-24 rounded-md border px-3 py-2 text-xs"
              disabled={completing}
            />
          </div>
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button type="button" onClick={handleComplete} disabled={completing || !callbackUrl.trim()}>
            {completing ? '添加中...' : '添加账号'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
