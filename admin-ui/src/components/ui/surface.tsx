import * as Dialog from '@radix-ui/react-dialog'
import * as Menu from '@radix-ui/react-dropdown-menu'
import { MoreHorizontal, X } from 'lucide-react'
import type { ReactNode, ThHTMLAttributes, TdHTMLAttributes } from 'react'
import { Toaster as Sonner } from 'sonner'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'

/** 从右侧滑入的玻璃抽屉 */
export function Sheet({
  open,
  onOpenChange,
  children,
  wide = false,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  children: ReactNode
  wide?: boolean
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="overlay fixed inset-0 z-40 bg-black/10 dark:bg-black/40" />
        <Dialog.Content
          aria-describedby={undefined}
          className={cn(
            'sheet glass glass-strong fixed bottom-2 right-2 top-2 z-50 flex flex-col rounded-[18px] outline-none',
            wide ? 'w-[min(880px,calc(100vw-16px))]' : 'w-[min(520px,calc(100vw-16px))]',
          )}
        >
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

export function SheetHeader({ title, subtitle, children }: { title: ReactNode; subtitle?: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex items-start gap-3 border-b border-[var(--line)] px-6 py-4">
      <div className="min-w-0 flex-1">
        <Dialog.Title className="truncate text-[17px] font-semibold tracking-tight">{title}</Dialog.Title>
        {subtitle ? <div className="mt-1 text-[13px] text-fg2">{subtitle}</div> : null}
      </div>
      {children}
      <Dialog.Close asChild>
        <Button variant="plain" size="icon" aria-label="关闭" className="-mr-2">
          <X className="h-4 w-4" />
        </Button>
      </Dialog.Close>
    </div>
  )
}

export function SheetBody({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('min-h-0 flex-1 space-y-6 overflow-y-auto px-6 py-5', className)}>{children}</div>
}

/** 区块标题 + 右侧操作 */
export function SectionHeader({ title, children }: { title: ReactNode; children?: ReactNode }) {
  return (
    <div className="mb-3 flex min-h-8 flex-wrap items-center justify-between gap-3">
      <h2 className="text-[15px] font-semibold tracking-tight">{title}</h2>
      {children ? <div className="flex flex-wrap items-center gap-2">{children}</div> : null}
    </div>
  )
}

export function Panel({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('glass rounded-2xl', className)}>{children}</div>
}

/** 数据表：玻璃面板 + 横向可滚动 */
export function Table({ children, className, minWidth = 720 }: { children: ReactNode; className?: string; minWidth?: number }) {
  return (
    <div className={cn('glass overflow-hidden rounded-2xl', className)}>
      <div className="overflow-x-auto">
        <table className="w-full border-collapse text-left text-[13px]" style={{ minWidth }}>
          {children}
        </table>
      </div>
    </div>
  )
}

export function THead({ children }: { children: ReactNode }) {
  return (
    <thead className="border-b border-[var(--line)] text-[12px] text-fg2">
      <tr>{children}</tr>
    </thead>
  )
}

export function Th({ className, children, ...props }: ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th className={cn('h-10 whitespace-nowrap px-3 font-medium first:pl-5 last:pr-5', className)} {...props}>
      {children}
    </th>
  )
}

export function Tr({ onClick, selected, children, className }: { onClick?: () => void; selected?: boolean; children: ReactNode; className?: string }) {
  return (
    <tr
      onClick={onClick}
      className={cn('border-b border-[var(--line)] transition-colors last:border-0', onClick && 'cursor-default hover:bg-fill', selected && 'bg-fill', className)}
    >
      {children}
    </tr>
  )
}

export function Td({ className, children, ...props }: TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <td className={cn('h-12 whitespace-nowrap px-3 first:pl-5 last:pr-5', className)} {...props}>
      {children}
    </td>
  )
}

export function EmptyRow({ colSpan, children }: { colSpan: number; children: ReactNode }) {
  return (
    <tr>
      <td colSpan={colSpan} className="px-5 py-14 text-center text-fg2">
        {children}
      </td>
    </tr>
  )
}

/** 抽屉里的属性列表：左边名称，右边值 */
export function Props({ children }: { children: ReactNode }) {
  return <dl className="grid grid-cols-[112px_minmax(0,1fr)] items-center gap-x-4 gap-y-3 text-[14px]">{children}</dl>
}

export function Prop({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <>
      <dt className="text-[13px] text-fg2">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </>
  )
}

export function Stat({ label, value, note }: { label: string; value: ReactNode; note?: ReactNode }) {
  return (
    <div className="rounded-xl bg-fill px-3.5 py-3">
      <div className="text-[12px] text-fg2">{label}</div>
      <div className="tnum mt-1 truncate text-[18px] font-semibold tracking-tight">{value}</div>
      {note ? <div className="mt-0.5 truncate text-[12px] text-fg3">{note}</div> : null}
    </div>
  )
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 px-6 py-14 text-center">
      <p className="text-fg2">{title}</p>
      {children}
    </div>
  )
}

/** 行尾的“更多”菜单 */
export function RowMenu({ children }: { children: ReactNode }) {
  return (
    <Menu.Root>
      <Menu.Trigger asChild>
        <Button variant="plain" size="iconXs" aria-label="更多操作" onClick={(event) => event.stopPropagation()}>
          <MoreHorizontal className="h-4 w-4" />
        </Button>
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content align="end" sideOffset={6} className="pop glass glass-strong z-50 min-w-40 rounded-xl p-1 outline-none" onClick={(event) => event.stopPropagation()}>
          {children}
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  )
}

export function MenuItem({ children, onSelect, danger, disabled }: { children: ReactNode; onSelect: () => void; danger?: boolean; disabled?: boolean }) {
  return (
    <Menu.Item
      disabled={disabled}
      onSelect={onSelect}
      className={cn(
        'flex h-8 cursor-default select-none items-center gap-2 rounded-[7px] px-2.5 text-[13px] outline-none data-[disabled]:opacity-40 data-[highlighted]:bg-fill',
        danger ? 'text-bad' : 'text-fg',
      )}
    >
      {children}
    </Menu.Item>
  )
}

export function MenuSeparator() {
  return <Menu.Separator className="mx-1 my-1 h-px bg-[var(--line)]" />
}

export function Toaster() {
  return (
    <Sonner
      position="bottom-right"
      gap={8}
      toastOptions={{
        unstyled: true,
        classNames: {
          toast: 'glass glass-strong flex w-[min(360px,calc(100vw-32px))] items-center gap-2.5 rounded-xl px-4 py-3 text-[13px] text-fg',
          title: 'font-medium',
          description: 'text-fg2 text-[12px]',
          success: '[&_[data-icon]]:text-ok',
          error: '[&_[data-icon]]:text-bad',
          warning: '[&_[data-icon]]:text-warn',
          info: '[&_[data-icon]]:text-accent',
          actionButton: 'ml-auto h-7 shrink-0 rounded-[7px] bg-bad px-2.5 text-[12px] font-medium text-white',
          cancelButton: 'h-7 shrink-0 rounded-[7px] bg-fill px-2.5 text-[12px] font-medium text-fg',
        },
      }}
    />
  )
}
