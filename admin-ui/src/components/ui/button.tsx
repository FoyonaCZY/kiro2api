import { forwardRef, useEffect, useState, type ButtonHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

const variants = {
  primary: 'bg-accent text-white shadow-[0_6px_18px_-8px_var(--accent)] hover:brightness-110',
  glass: 'glass text-fg hover:bg-[var(--glass-strong)]',
  tinted: 'bg-fill text-fg hover:bg-fill-strong',
  plain: 'text-fg2 hover:bg-fill hover:text-fg',
  link: 'text-accent hover:bg-fill',
  danger: 'bg-fill text-bad hover:bg-fill-strong',
  solidDanger: 'bg-bad text-white hover:brightness-110',
}

const sizes = {
  xs: 'h-7 px-2.5 text-[12px] rounded-[7px]',
  sm: 'h-8 px-3 text-[13px] rounded-[8px]',
  md: 'h-9 px-4 text-[14px] rounded-[9px]',
  icon: 'h-8 w-8 rounded-[8px]',
  iconXs: 'h-7 w-7 rounded-[7px]',
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: keyof typeof variants
  size?: keyof typeof sizes
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'tinted', size = 'sm', className, type = 'button', ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      className={cn(
        'inline-flex select-none items-center justify-center gap-1.5 whitespace-nowrap font-medium transition duration-150 active:scale-[.98] disabled:pointer-events-none disabled:opacity-40',
        variants[variant],
        sizes[size],
        className,
      )}
      {...props}
    />
  )
})

/** 危险操作的两步确认：点一次变红，三秒内再点才执行 */
export function ConfirmButton({
  onConfirm,
  children,
  confirmText = '确认删除',
  size = 'sm',
  className,
  disabled,
}: {
  onConfirm: () => void
  children: React.ReactNode
  confirmText?: string
  size?: ButtonProps['size']
  className?: string
  disabled?: boolean
}) {
  const [armed, setArmed] = useState(false)
  useEffect(() => {
    if (!armed) return
    const t = setTimeout(() => setArmed(false), 3000)
    return () => clearTimeout(t)
  }, [armed])
  return (
    <Button
      size={size}
      disabled={disabled}
      variant={armed ? 'solidDanger' : 'danger'}
      className={className}
      onClick={(event) => {
        event.stopPropagation()
        if (armed) {
          setArmed(false)
          onConfirm()
        } else {
          setArmed(true)
        }
      }}
    >
      {armed ? confirmText : children}
    </Button>
  )
}
