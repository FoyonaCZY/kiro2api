import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  createAccessGroup,
  createAccessKey,
  deleteAccessGroup,
  deleteAccessKey,
  getAccessGroups,
  getAccessKeys,
  renameAccessGroup,
  setGroupMembers,
  updateAccessKey,
  type AccessGroup,
  type AccessKey,
} from '@/api/access'
import { getCredentials } from '@/api/credentials'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { extractErrorMessage } from '@/lib/utils'

export function AccessPage() {
  const queryClient = useQueryClient()
  const keys = useQuery({ queryKey: ['access-keys'], queryFn: getAccessKeys })
  const groups = useQuery({ queryKey: ['access-groups'], queryFn: getAccessGroups })
  const credentials = useQuery({ queryKey: ['credentials'], queryFn: getCredentials })
  const [groupName, setGroupName] = useState('')
  const [selectedGroup, setSelectedGroup] = useState<AccessGroup | null>(null)
  const [memberDraft, setMemberDraft] = useState<number[]>([])
  const [keyForm, setKeyForm] = useState<AccessKey | 'new' | null>(null)
  const [keyName, setKeyName] = useState('')
  const [keyGroup, setKeyGroup] = useState('default')
  const [revealed, setRevealed] = useState<number | null>(null)
  const [created, setCreated] = useState('')

  function refresh() {
    queryClient.invalidateQueries({ queryKey: ['access-keys'] })
    queryClient.invalidateQueries({ queryKey: ['access-groups'] })
  }

  const addGroup = useMutation({
    mutationFn: () => createAccessGroup(groupName),
    onSuccess: () => {
      setGroupName('')
      refresh()
      toast.success('已创建分组')
    },
    onError: (error) => toast.error(extractErrorMessage(error)),
  })

  const saveKey = useMutation({
    mutationFn: async () => {
      if (keyForm === 'new') return createAccessKey({ name: keyName, groupId: keyGroup })
      if (keyForm) await updateAccessKey(keyForm.id, { name: keyName, groupId: keyGroup })
      return null
    },
    onSuccess: (createdKey) => {
      setKeyForm(null)
      refresh()
      if (createdKey) setCreated(createdKey.secret)
      else toast.success('已保存')
    },
    onError: (error) => toast.error(extractErrorMessage(error)),
  })

  function openGroup(group: AccessGroup) {
    setSelectedGroup(group)
    setMemberDraft(group.members)
  }

  async function saveMembers() {
    if (!selectedGroup) return
    try {
      await setGroupMembers(selectedGroup.id, memberDraft)
      refresh()
      setSelectedGroup(null)
      toast.success('分组成员已更新')
    } catch (error) {
      toast.error(extractErrorMessage(error))
    }
  }

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <PageHeader title="调度分组" description="点一行管理成员。没进任何分组的账号属于默认组，移出最后一个组后也会回到默认组。">
          <form className="flex gap-2" onSubmit={(event) => { event.preventDefault(); addGroup.mutate() }}>
            <Input value={groupName} onChange={(event) => setGroupName(event.target.value)} placeholder="新分组名称" className="w-44" />
            <Button type="submit" size="sm" disabled={!groupName.trim() || addGroup.isPending}>新建</Button>
          </form>
        </PageHeader>
        <div className="overflow-x-auto rounded-md border">
          <table className="w-full text-left text-sm">
            <thead className="border-b bg-card text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">名称</th>
                <th className="px-3 py-2 font-medium">账号</th>
                <th className="px-3 py-2 font-medium">密钥</th>
                <th className="px-3 py-2 font-medium"></th>
              </tr>
            </thead>
            <tbody>
              {(groups.data ?? []).map((group) => (
                <tr key={group.id} className="cursor-pointer border-b last:border-0 hover:bg-secondary/40" onClick={() => openGroup(group)}>
                  <td className="px-3 py-2">{group.name}{group.isDefault && group.name !== '默认' ? <span className="ml-2 text-xs text-muted-foreground">默认</span> : null}</td>
                  <td className="px-3 py-2">{group.members.length}</td>
                  <td className="px-3 py-2">{group.keyCount}</td>
                  <td className="px-3 py-2 text-right">
                    <Button size="sm" variant="outline" onClick={(event) => { event.stopPropagation(); openGroup(group) }}>成员</Button>
                    {!group.isDefault ? (
                      <Button size="sm" variant="ghost" onClick={(event) => {
                        event.stopPropagation()
                        deleteAccessGroup(group.id).then(() => { refresh(); toast.success('已删除') }).catch((error) => toast.error(extractErrorMessage(error)))
                      }}>删除</Button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="space-y-3">
        <PageHeader title="接入密钥" description="每把密钥绑定一个调度分组，请求只从那个组里拿号。">
          <Button size="sm" onClick={() => { setKeyForm('new'); setKeyName(''); setKeyGroup('default') }}>新增</Button>
        </PageHeader>
        <div className="overflow-x-auto rounded-md border">
          <table className="w-full min-w-[720px] text-left text-sm">
            <thead className="border-b bg-card text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">名称</th>
                <th className="px-3 py-2 font-medium">Key</th>
                <th className="px-3 py-2 font-medium">调度分组</th>
                <th className="px-3 py-2 font-medium">状态</th>
                <th className="px-3 py-2 font-medium"></th>
              </tr>
            </thead>
            <tbody>
              {(keys.data ?? []).map((key) => (
                <tr key={key.id} className="border-b last:border-0">
                  <td className="px-3 py-2">{key.name}</td>
                  <td className="px-3 py-2 font-mono text-xs">
                    {revealed === key.id ? key.secret : key.prefix}
                    <button type="button" className="ml-2 text-muted-foreground" onClick={() => setRevealed(revealed === key.id ? null : key.id)}>{revealed === key.id ? '隐藏' : '显示'}</button>
                    <button type="button" className="ml-2 text-muted-foreground" onClick={() => navigator.clipboard.writeText(key.secret).then(() => toast.success('已复制')).catch(() => toast.error('复制失败'))}>复制</button>
                  </td>
                  <td className="px-3 py-2">{key.groupName}</td>
                  <td className="px-3 py-2">{key.disabled ? '已停用' : '启用'}</td>
                  <td className="px-3 py-2 text-right">
                    <Button size="sm" variant="outline" onClick={() => { setKeyForm(key); setKeyName(key.name); setKeyGroup(key.groupId) }}>编辑</Button>
                    <Button size="sm" variant="ghost" onClick={() => updateAccessKey(key.id, { disabled: !key.disabled }).then(refresh).catch((error) => toast.error(extractErrorMessage(error)))}>{key.disabled ? '启用' : '停用'}</Button>
                    <Button size="sm" variant="ghost" onClick={() => deleteAccessKey(key.id).then(() => { refresh(); toast.success('已删除') }).catch((error) => toast.error(extractErrorMessage(error)))}>删除</Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <Dialog open={selectedGroup != null} onOpenChange={(open) => { if (!open) setSelectedGroup(null) }}>
        <DialogContent className="max-h-[86vh] overflow-auto sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{selectedGroup?.name || '调度分组'}</DialogTitle>
          </DialogHeader>
          <Input
            key={selectedGroup?.id}
            defaultValue={selectedGroup?.name}
            onBlur={(event) => {
              const next = event.target.value.trim()
              if (selectedGroup && next && next !== selectedGroup.name) {
                renameAccessGroup(selectedGroup.id, next).then(refresh).catch((error) => toast.error(extractErrorMessage(error)))
              }
            }}
          />
          <p className="text-sm text-muted-foreground">勾选这个组要调度的账号。</p>
          <div className="grid gap-2">
            {(credentials.data?.credentials ?? []).map((credential) => (
              <label key={credential.id} className="flex items-center gap-2 rounded-md border px-3 py-2 text-sm">
                <input
                  type="checkbox"
                  checked={memberDraft.includes(credential.id)}
                  onChange={(event) => setMemberDraft((current) => event.target.checked ? [...current, credential.id] : current.filter((id) => id !== credential.id))}
                />
                <span>{credential.email || `#${credential.id}`}</span>
              </label>
            ))}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setSelectedGroup(null)}>取消</Button>
            <Button onClick={saveMembers}>保存成员</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={keyForm != null} onOpenChange={(open) => { if (!open) setKeyForm(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{keyForm === 'new' ? '新增接入密钥' : '编辑接入密钥'}</DialogTitle>
          </DialogHeader>
          <label className="space-y-1 text-sm">
            <span>名称</span>
            <Input value={keyName} onChange={(event) => setKeyName(event.target.value)} placeholder="例如 家里、公司" />
          </label>
          <label className="space-y-1 text-sm">
            <span>调度分组</span>
            <select className="h-9 w-full rounded-md border bg-background px-2" value={keyGroup} onChange={(event) => setKeyGroup(event.target.value)}>
              {(groups.data ?? []).map((group) => <option key={group.id} value={group.id}>{group.name}</option>)}
            </select>
          </label>
          <DialogFooter>
            <Button variant="outline" onClick={() => setKeyForm(null)}>取消</Button>
            <Button disabled={!keyName.trim() || saveKey.isPending} onClick={() => saveKey.mutate()}>{keyForm === 'new' ? '添加' : '保存'}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={created !== ''} onOpenChange={(open) => { if (!open) setCreated('') }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>密钥已创建</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">之后也能在列表里查看和复制。</p>
          <code className="block break-all rounded-md border bg-card p-3 text-sm">{created}</code>
          <DialogFooter>
            <Button onClick={() => navigator.clipboard.writeText(created).then(() => toast.success('已复制'))}>复制</Button>
            <Button variant="outline" onClick={() => setCreated('')}>知道了</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
