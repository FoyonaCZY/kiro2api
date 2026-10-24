import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Copy, Eye, EyeOff, Plus } from 'lucide-react'
import { createAccessGroup, createAccessKey, deleteAccessGroup, deleteAccessKey, getAccessGroups, getAccessKeys, renameAccessGroup, setGroupMembers, updateAccessKey, type AccessGroup, type AccessKey } from '@/api/access'
import { getCredentials } from '@/api/credentials'
import { accountName } from '@/lib/format'
import { extractErrorMessage } from '@/lib/utils'
import { PageTitle } from '@/components/shell'
import { Button, ConfirmButton } from '@/components/ui/button'
import { Badge, Checkbox, Spinner, Switch } from '@/components/ui/controls'
import { Field, Input, Select } from '@/components/ui/field'
import { EmptyRow, MenuItem, MenuSeparator, RowMenu, SectionHeader, Sheet, SheetBody, SheetHeader, Table, Td, Th, THead, Tr } from '@/components/ui/surface'

function copy(text: string) {
  navigator.clipboard.writeText(text).then(() => toast.success('已复制')).catch(() => toast.error('浏览器不允许复制'))
}

export function KeysPage() {
  const queryClient = useQueryClient()
  const keys = useQuery({ queryKey: ['access-keys'], queryFn: getAccessKeys })
  const groups = useQuery({ queryKey: ['access-groups'], queryFn: getAccessGroups })
  const [keySheet, setKeySheet] = useState<AccessKey | 'new' | null>(null)
  const [groupSheet, setGroupSheet] = useState<AccessGroup | 'new' | null>(null)
  const [revealed, setRevealed] = useState<number | null>(null)

  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['access-keys'] })
    queryClient.invalidateQueries({ queryKey: ['access-groups'] })
  }
  const run = (p: Promise<unknown>, ok?: string) => p.then(() => { refresh(); if (ok) toast.success(ok) }).catch((error) => toast.error(extractErrorMessage(error)))
  const askDelete = (k: AccessKey) =>
    toast(`删除密钥「${k.name}」？`, {
      description: '用这个密钥的客户端会立即失效',
      action: { label: '删除', onClick: () => run(deleteAccessKey(k.id), '已删除') },
      cancel: { label: '取消', onClick: () => undefined },
    })

  if (keys.isLoading || groups.isLoading) return <div className="flex justify-center py-32"><Spinner className="h-6 w-6" /></div>

  return (
    <div className="space-y-8">
      <div>
        <PageTitle title="密钥" subtitle="每个密钥只用所属分组里的账号">
          <Button variant="primary" size="md" onClick={() => setKeySheet('new')}>
            <Plus className="h-4 w-4" />
            新建密钥
          </Button>
        </PageTitle>
        <Table minWidth={640}>
          <THead>
            <Th>名称</Th>
            <Th>密钥</Th>
            <Th>分组</Th>
            <Th className="w-14">启用</Th>
            <Th className="w-10" />
          </THead>
          <tbody>
            {(keys.data ?? []).length === 0 ? <EmptyRow colSpan={5}>还没有密钥</EmptyRow> : null}
            {(keys.data ?? []).map((k) => (
              <Tr key={k.id} onClick={() => setKeySheet(k)}>
                <Td className={k.disabled ? 'font-medium text-fg2' : 'font-medium'}>{k.name}</Td>
                <Td>
                  <div className="flex items-center gap-1">
                    <code className="font-mono text-[12px] text-fg2">{revealed === k.id ? k.secret : `${k.prefix}…`}</code>
                    <Button variant="plain" size="iconXs" aria-label={revealed === k.id ? '隐藏' : '显示'} onClick={(e) => { e.stopPropagation(); setRevealed(revealed === k.id ? null : k.id) }}>
                      {revealed === k.id ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                    </Button>
                    <Button variant="plain" size="iconXs" aria-label="复制" onClick={(e) => { e.stopPropagation(); copy(k.secret) }}>
                      <Copy className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </Td>
                <Td className="text-fg2">{k.groupName}</Td>
                <Td><Switch label="启用" checked={!k.disabled} onCheckedChange={(on) => run(updateAccessKey(k.id, { disabled: !on }))} /></Td>
                <Td>
                  <RowMenu>
                    <MenuItem onSelect={() => setKeySheet(k)}>编辑</MenuItem>
                    <MenuItem onSelect={() => copy(k.secret)}>复制密钥</MenuItem>
                    <MenuSeparator />
                    <MenuItem danger onSelect={() => askDelete(k)}>删除</MenuItem>
                  </RowMenu>
                </Td>
              </Tr>
            ))}
          </tbody>
        </Table>
      </div>

      <section>
        <SectionHeader title="分组">
          <Button onClick={() => setGroupSheet('new')}>
            <Plus className="h-3.5 w-3.5" />
            新建分组
          </Button>
        </SectionHeader>
        <Table minWidth={480}>
          <THead>
            <Th>名称</Th>
            <Th className="text-right">账号</Th>
            <Th className="text-right">密钥</Th>
          </THead>
          <tbody>
            {(groups.data ?? []).map((g) => (
              <Tr key={g.id} onClick={() => setGroupSheet(g)}>
                <Td>
                  <span className="font-medium">{g.name}</span>
                  {g.isDefault && g.name !== '默认' ? <Badge className="ml-2">默认</Badge> : null}
                </Td>
                <Td className="tnum text-right text-fg2">{g.isDefault && g.members.length === 0 ? '未分组的账号' : g.members.length}</Td>
                <Td className="tnum text-right text-fg2">{g.keyCount}</Td>
              </Tr>
            ))}
          </tbody>
        </Table>
      </section>

      <KeySheet value={keySheet} groups={groups.data ?? []} onClose={() => setKeySheet(null)} onSaved={refresh} />
      <GroupSheet value={groupSheet} onClose={() => setGroupSheet(null)} onSaved={refresh} />
    </div>
  )
}

function KeySheet({ value, groups, onClose, onSaved }: { value: AccessKey | 'new' | null; groups: AccessGroup[]; onClose: () => void; onSaved: () => void }) {
  return (
    <Sheet open={value != null} onOpenChange={(open) => { if (!open) onClose() }}>
      {value ? <KeyForm key={value === 'new' ? 'new' : value.id} value={value === 'new' ? null : value} groups={groups} onClose={onClose} onSaved={onSaved} /> : null}
    </Sheet>
  )
}

function KeyForm({ value, groups, onClose, onSaved }: { value: AccessKey | null; groups: AccessGroup[]; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(value?.name ?? '')
  const [group, setGroup] = useState(value?.groupId ?? groups.find((g) => g.isDefault)?.id ?? 'default')
  const [created, setCreated] = useState<AccessKey | null>(null)
  const [saving, setSaving] = useState(false)

  const save = async () => {
    setSaving(true)
    try {
      if (value) {
        await updateAccessKey(value.id, { name: name.trim(), groupId: group })
        toast.success('已保存')
        onClose()
      } else {
        setCreated(await createAccessKey({ name: name.trim(), groupId: group }))
      }
      onSaved()
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  if (created) {
    return (
      <>
        <SheetHeader title="密钥已创建" subtitle={created.name} />
        <SheetBody>
          <code className="block break-all rounded-xl bg-fill p-4 font-mono text-[13px]">{created.secret}</code>
          <Button variant="primary" size="md" className="w-full" onClick={() => copy(created.secret)}>
            <Copy className="h-4 w-4" />
            复制
          </Button>
        </SheetBody>
      </>
    )
  }

  return (
    <>
      <SheetHeader title={value ? '编辑密钥' : '新建密钥'} />
      <SheetBody>
        <form className="grid gap-4" onSubmit={(event) => { event.preventDefault(); if (name.trim()) save() }}>
          <Field label="名称"><Input placeholder="比如 家里、公司" value={name} onChange={(e) => setName(e.target.value)} autoFocus={!value} /></Field>
          <Field label="分组">
            <Select value={group} onChange={(e) => setGroup(e.target.value)}>
              {groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
            </Select>
          </Field>
          <div className="mt-2 flex gap-2">
            {value ? (
              <ConfirmButton size="md" onConfirm={() => deleteAccessKey(value.id).then(() => { onSaved(); toast.success('已删除'); onClose() }).catch((error) => toast.error(extractErrorMessage(error)))}>
                删除
              </ConfirmButton>
            ) : null}
            <Button type="submit" variant="primary" size="md" className="flex-1" disabled={!name.trim() || saving}>
              {value ? '保存' : '创建'}
            </Button>
          </div>
        </form>
      </SheetBody>
    </>
  )
}

function GroupSheet({ value, onClose, onSaved }: { value: AccessGroup | 'new' | null; onClose: () => void; onSaved: () => void }) {
  return (
    <Sheet open={value != null} onOpenChange={(open) => { if (!open) onClose() }}>
      {value ? <GroupForm key={value === 'new' ? 'new' : value.id} value={value === 'new' ? null : value} onClose={onClose} onSaved={onSaved} /> : null}
    </Sheet>
  )
}

function GroupForm({ value, onClose, onSaved }: { value: AccessGroup | null; onClose: () => void; onSaved: () => void }) {
  const credentials = useQuery({ queryKey: ['credentials'], queryFn: getCredentials })
  const [name, setName] = useState(value?.name ?? '')
  const [members, setMembers] = useState<number[]>(value?.members ?? [])
  const [saving, setSaving] = useState(false)

  const save = async () => {
    setSaving(true)
    try {
      const target = value ?? (await createAccessGroup(name.trim()))
      if (value && name.trim() && name.trim() !== value.name) await renameAccessGroup(value.id, name.trim())
      await setGroupMembers(target.id, members)
      onSaved()
      toast.success('已保存')
      onClose()
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  const list = credentials.data?.credentials ?? []
  return (
    <>
      <SheetHeader title={value ? value.name : '新建分组'} subtitle={value?.isDefault ? '没有加入任何分组的账号都在这里' : undefined} />
      <SheetBody>
        <Field label="名称"><Input value={name} onChange={(e) => setName(e.target.value)} autoFocus={!value} /></Field>
        <section>
          <div className="mb-2 flex items-center justify-between">
            <h3 className="text-[13px] font-medium text-fg2">账号 {members.length}/{list.length}</h3>
            <Button variant="link" size="xs" onClick={() => setMembers(members.length === list.length ? [] : list.map((c) => c.id))}>
              {members.length === list.length ? '全不选' : '全选'}
            </Button>
          </div>
          <div className="divide-y divide-[var(--line)] overflow-hidden rounded-xl bg-fill">
            {list.map((c) => {
              const on = members.includes(c.id)
              const flip = () => setMembers(on ? members.filter((id) => id !== c.id) : [...members, c.id])
              return (
                <div key={c.id} role="button" tabIndex={0} onClick={flip} onKeyDown={(e) => { if (e.key === ' ' || e.key === 'Enter') flip() }} className="flex cursor-default items-center gap-3 px-3.5 py-2 text-[14px] hover:bg-fill">
                  <Checkbox checked={on} onChange={flip} />
                  <span className="truncate">{accountName(c)}</span>
                </div>
              )
            })}
          </div>
        </section>
        <div className="flex gap-2">
          {value && !value.isDefault ? (
            <ConfirmButton size="md" onConfirm={() => deleteAccessGroup(value.id).then(() => { onSaved(); toast.success('已删除'); onClose() }).catch((error) => toast.error(extractErrorMessage(error)))}>
              删除
            </ConfirmButton>
          ) : null}
          <Button variant="primary" size="md" className="flex-1" disabled={!name.trim() || saving} onClick={save}>保存</Button>
        </div>
      </SheetBody>
    </>
  )
}
