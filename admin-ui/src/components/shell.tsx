import { useEffect, useState, type ReactNode } from 'react'
import { Activity, KeyRound, LogOut, Moon, Sun, Users, Wallet } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'

export type Page = 'accounts' | 'requests' | 'usage' | 'keys'

const pages: { id: Page; label: string; icon: typeof Users }[] = [
  { id: 'accounts', label: '账号', icon: Users },
  { id: 'requests', label: '请求', icon: Activity },
  { id: 'usage', label: '用量', icon: Wallet },
  { id: 'keys', label: '密钥', icon: KeyRound },
]

// 旧版的 hash 继续能打开对应页面
const legacy: Record<string, Page> = { credentials: 'accounts', access: 'keys', prices: 'usage' }

function readPage(): Page {
  const hash = window.location.hash.replace(/^#/, '')
  if (pages.some((p) => p.id === hash)) return hash as Page
  return legacy[hash] ?? 'accounts'
}

export function usePage(): [Page, (p: Page) => void] {
  const [page, setPage] = useState<Page>(readPage)
  useEffect(() => {
    const sync = () => setPage(readPage())
    window.addEventListener('hashchange', sync)
    return () => window.removeEventListener('hashchange', sync)
  }, [])
  return [page, (p) => { window.location.hash = p }]
}

function useTheme(): [boolean, () => void] {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  const toggle = () => {
    const next = !dark
    setDark(next)
    document.documentElement.classList.toggle('dark', next)
    try {
      localStorage.setItem('kiro-admin-theme', next ? 'dark' : 'light')
    } catch {
      // 存不了就只在本次生效
    }
  }
  return [dark, toggle]
}

export function AppMark({ className }: { className?: string }) {
  return <img src={`${import.meta.env.BASE_URL}icon.svg`} alt="" className={cn('h-7 w-7 rounded-[8px]', className)} />
}

export function Shell({ page, onNavigate, onLogout, children }: { page: Page; onNavigate: (p: Page) => void; onLogout: () => void; children: ReactNode }) {
  const [dark, toggleTheme] = useTheme()
  const item = (active: boolean) =>
    cn('flex h-9 w-full items-center gap-2.5 rounded-[9px] px-2.5 text-[14px] transition-colors', active ? 'bg-fill-strong font-medium text-fg' : 'text-fg2 hover:bg-fill hover:text-fg')

  return (
    <div className="min-h-screen md:pl-[248px]">
      <aside className="glass fixed bottom-3 left-3 top-3 z-30 hidden w-[224px] flex-col rounded-2xl p-3 md:flex">
        <div className="flex items-center gap-2.5 px-1.5 pb-5 pt-1">
          <AppMark />
          <span className="text-[15px] font-semibold tracking-tight">kiro2api</span>
        </div>
        <nav className="grid gap-0.5">
          {pages.map(({ id, label, icon: Icon }) => (
            <button key={id} type="button" className={item(page === id)} aria-current={page === id ? 'page' : undefined} onClick={() => onNavigate(id)}>
              <Icon className="h-4 w-4" />
              {label}
            </button>
          ))}
        </nav>
        <div className="mt-auto grid gap-0.5 border-t border-[var(--line)] pt-3">
          <button type="button" className={item(false)} onClick={toggleTheme}>
            {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            {dark ? '浅色模式' : '深色模式'}
          </button>
          <button type="button" className={item(false)} onClick={onLogout}>
            <LogOut className="h-4 w-4" />
            退出登录
          </button>
        </div>
      </aside>

      <header className="glass sticky top-0 z-30 flex h-12 items-center gap-1 px-3 md:hidden">
        <AppMark className="mr-1 h-6 w-6" />
        {pages.map(({ id, label }) => (
          <button
            key={id}
            type="button"
            onClick={() => onNavigate(id)}
            className={cn('h-8 rounded-[8px] px-2.5 text-[13px]', page === id ? 'bg-fill-strong font-medium text-fg' : 'text-fg2')}
          >
            {label}
          </button>
        ))}
        <Button variant="plain" size="icon" className="ml-auto" onClick={toggleTheme} aria-label="切换主题">
          {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
        </Button>
        <Button variant="plain" size="icon" onClick={onLogout} aria-label="退出登录">
          <LogOut className="h-4 w-4" />
        </Button>
      </header>

      <main className="mx-auto max-w-[1480px] px-4 py-6 md:px-8 md:py-7">{children}</main>
    </div>
  )
}

/** 页面标题，右侧放主要操作 */
export function PageTitle({ title, subtitle, children }: { title: string; subtitle?: ReactNode; children?: ReactNode }) {
  return (
    <div className="mb-5 flex min-h-9 flex-wrap items-center justify-between gap-3">
      <div className="flex items-baseline gap-3">
        <h1 className="text-[22px] font-semibold tracking-tight">{title}</h1>
        {subtitle ? <span className="text-[13px] text-fg2">{subtitle}</span> : null}
      </div>
      {children ? <div className="flex flex-wrap items-center gap-2">{children}</div> : null}
    </div>
  )
}
