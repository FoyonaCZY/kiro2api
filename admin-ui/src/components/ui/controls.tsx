import * as SwitchPrimitive from '@radix-ui/react-switch'
import { Check, Minus } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface SegmentOption<T extends string> {
  value: T
  label: React.ReactNode
}

/** 分段控件，滑块跟着选中项移动 */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  className,
  disabled,
}: {
  value: T
  onChange: (value: T) => void
  options: SegmentOption<T>[]
  className?: string
  disabled?: boolean
}) {
  const index = options.findIndex((option) => option.value === value)
  return (
    <div role="tablist" className={cn('relative inline-grid auto-cols-fr grid-flow-col rounded-[9px] bg-fill p-[2px]', disabled && 'opacity-50', className)}>
      {index >= 0 ? (
        <span
          aria-hidden
          className="absolute bottom-[2px] left-[2px] top-[2px] rounded-[7px] bg-[var(--thumb)] shadow-[0_1px_4px_rgba(0,0,0,.12)] transition-transform duration-300 ease-[cubic-bezier(.2,.9,.25,1.1)]"
          style={{ width: `calc((100% - 4px) / ${options.length})`, transform: `translateX(${index * 100}%)` }}
        />
      ) : null}
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          role="tab"
          aria-selected={option.value === value}
          disabled={disabled}
          className={cn(
            'relative z-10 flex h-7 items-center justify-center gap-1 whitespace-nowrap rounded-[7px] px-3 text-[13px] font-medium transition-colors',
            option.value === value ? 'text-fg' : 'text-fg2 hover:text-fg',
          )}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}

export function Switch({ checked, onCheckedChange, disabled, label }: { checked: boolean; onCheckedChange: (v: boolean) => void; disabled?: boolean; label?: string }) {
  return (
    <SwitchPrimitive.Root
      checked={checked}
      onCheckedChange={onCheckedChange}
      disabled={disabled}
      aria-label={label}
      onClick={(event) => event.stopPropagation()}
      className="relative h-[20px] w-[34px] shrink-0 rounded-full bg-fill-strong transition-colors duration-200 data-[state=checked]:bg-ok disabled:opacity-50"
    >
      <SwitchPrimitive.Thumb className="block h-4 w-4 translate-x-[2px] rounded-full bg-white shadow-[0_1px_3px_rgba(0,0,0,.3)] transition-transform duration-200 ease-[cubic-bezier(.2,.9,.25,1.2)] data-[state=checked]:translate-x-[16px]" />
    </SwitchPrimitive.Root>
  )
}

/** 方形勾选框，支持半选 */
export function Checkbox({ checked, indeterminate, onChange, label }: { checked: boolean; indeterminate?: boolean; onChange: () => void; label?: string }) {
  const on = checked || indeterminate
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={indeterminate ? 'mixed' : checked}
      aria-label={label}
      onClick={(event) => {
        event.stopPropagation()
        onChange()
      }}
      className={cn(
        'flex h-4 w-4 shrink-0 items-center justify-center rounded-[5px] border-[1.5px] transition',
        on ? 'border-accent bg-accent text-white' : 'border-fg3 hover:border-fg2',
      )}
    >
      {indeterminate ? <Minus className="h-3 w-3" strokeWidth={3} /> : checked ? <Check className="h-3 w-3" strokeWidth={3} /> : null}
    </button>
  )
}

export function Spinner({ className }: { className?: string }) {
  return <span className={cn('inline-block h-4 w-4 animate-spin rounded-full border-2 border-fg3 border-t-fg', className)} />
}

export type Tone = 'ok' | 'warn' | 'bad' | 'off' | 'accent'

export function Dot({ tone, className }: { tone: Tone; className?: string }) {
  const color = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad', off: 'bg-fg3', accent: 'bg-accent' }[tone]
  return <span className={cn('inline-block h-2 w-2 shrink-0 rounded-full', color, className)} />
}

/** 小标签 */
export function Badge({ tone = 'off', children, className }: { tone?: Tone; children: React.ReactNode; className?: string }) {
  const color = {
    ok: 'text-ok bg-[color-mix(in_srgb,var(--ok)_14%,transparent)]',
    warn: 'text-warn bg-[color-mix(in_srgb,var(--warn)_14%,transparent)]',
    bad: 'text-bad bg-[color-mix(in_srgb,var(--bad)_14%,transparent)]',
    accent: 'text-accent bg-[color-mix(in_srgb,var(--accent)_14%,transparent)]',
    off: 'text-fg2 bg-fill',
  }[tone]
  return <span className={cn('inline-flex h-5 items-center whitespace-nowrap rounded-[6px] px-1.5 text-[11px] font-medium', color, className)}>{children}</span>
}

export function Meter({ value, tone = 'accent' }: { value: number; tone?: 'accent' | 'warn' | 'bad' }) {
  const color = { accent: 'bg-accent', warn: 'bg-warn', bad: 'bg-bad' }[tone]
  return (
    <div className="h-1.5 overflow-hidden rounded-full bg-fill">
      <div className={cn('h-full rounded-full transition-[width] duration-500', color)} style={{ width: `${Math.min(100, Math.max(0, value))}%` }} />
    </div>
  )
}
