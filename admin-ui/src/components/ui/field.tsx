import { forwardRef, useState, type InputHTMLAttributes, type ReactNode, type TextareaHTMLAttributes } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'

const control =
  'w-full rounded-[9px] border border-[var(--line)] bg-fill px-3 text-[14px] text-fg outline-none transition placeholder:text-fg3 focus:bg-fill-strong focus:ring-2 focus:ring-[color-mix(in_srgb,var(--accent)_55%,transparent)] disabled:opacity-50'

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function Input(
  { className, ...props },
  ref,
) {
  return <input ref={ref} className={cn(control, 'h-9', className)} {...props} />
})

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement>>(function Textarea(
  { className, ...props },
  ref,
) {
  return <textarea ref={ref} className={cn(control, 'min-h-28 resize-y py-2.5 font-mono text-[13px] leading-5', className)} {...props} />
})

export function Select({ className, children, ...props }: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className="relative inline-flex w-full">
      <select className={cn(control, 'h-9 cursor-pointer appearance-none pr-9', className)} {...props}>
        {children}
      </select>
      <ChevronDown className="pointer-events-none absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 text-fg2" />
    </span>
  )
}

export function Field({ label, hint, children, className }: { label: string; hint?: string; children: ReactNode; className?: string }) {
  return (
    <label className={cn('grid gap-1.5', className)}>
      <span className="text-[13px] font-medium text-fg2">{label}</span>
      {children}
      {hint ? <span className="text-[12px] text-fg3">{hint}</span> : null}
    </label>
  )
}

/** 折叠的次要设置 */
export function Disclosure({ title, children, defaultOpen = false }: { title: string; children: ReactNode; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div>
      <button type="button" className="flex items-center gap-1 text-[13px] font-medium text-accent" onClick={() => setOpen((v) => !v)}>
        <ChevronRight className={cn('h-4 w-4 transition-transform', open && 'rotate-90')} />
        {title}
      </button>
      {open ? <div className="mt-3 grid gap-3">{children}</div> : null}
    </div>
  )
}
