import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { getAccessGroups, setGroupMembers, type AccessGroup } from '@/api/access'
import { formatCNY, formatCostRatio, getAccountCosts, setAccountCost } from '@/api/usage'
import { RefreshCw, ChevronUp, ChevronDown, Wallet, Trash2, Pencil } from 'lucide-react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import type { CredentialStatusItem } from '@/types/api'
import { EditCredentialDialog } from '@/components/edit-credential-dialog'
import { extractErrorMessage } from '@/lib/utils'
import {
  useSetDisabled,
  useSetPriority,
  useResetFailure,
  useDeleteCredential,
  useForceRefreshToken,
} from '@/hooks/use-credentials'

interface CredentialCardProps {
  credential: CredentialStatusItem
  onViewBalance: (id: number) => void
  selected: boolean
  onToggleSelect: () => void
}

export function CredentialCard({
  credential,
  onViewBalance,
  selected,
  onToggleSelect,
}: CredentialCardProps) {
  const [editingPriority, setEditingPriority] = useState(false)
  const [priorityValue, setPriorityValue] = useState(String(credential.priority))
  const [editingCost, setEditingCost] = useState(false)
  const [costValue, setCostValue] = useState('')
  const [costSaving, setCostSaving] = useState(false)
  const [showDeleteDialog, setShowDeleteDialog] = useState(false)
  const [editOpen, setEditOpen] = useState(false)
  const [groupOpen, setGroupOpen] = useState(false)
  const [groupDraft, setGroupDraft] = useState<string[]>([])
  const [groupSaving, setGroupSaving] = useState(false)
  const queryClient = useQueryClient()
  const accessGroups = useQuery({ queryKey: ['access-groups'], queryFn: getAccessGroups })
  const joined = (accessGroups.data ?? []).filter((group) => group.members.includes(credential.id))
  // 与价格页共用同一份账号成本数据；消耗按当前单价重算
  const accountCosts = useQuery({ queryKey: ['usage-accounts'], queryFn: getAccountCosts })
  const cost = accountCosts.data?.accounts.find((row) => row.credentialId === credential.id)

  const startEditCost = () => {
    setCostValue(cost?.costCny == null ? '' : String(cost.costCny))
    setEditingCost(true)
  }

  const saveCost = async () => {
    const raw = costValue.trim()
    const value = raw === '' ? null : Number(raw)
    if (value !== null && (Number.isNaN(value) || value < 0)) {
      toast.error('成本要是非负数字；留空表示未知')
      return
    }
    setCostSaving(true)
    try {
      const next = await setAccountCost(credential.id, value)
      queryClient.setQueryData(['usage-accounts'], next)
      setEditingCost(false)
      toast.success(value === null ? '已清空账号成本' : '已保存账号成本')
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setCostSaving(false)
    }
  }

  const setDisabled = useSetDisabled()
  const setPriority = useSetPriority()
  const resetFailure = useResetFailure()
  const deleteCredential = useDeleteCredential()
  const forceRefresh = useForceRefreshToken()

  const handleToggleDisabled = () => {
    setDisabled.mutate(
      { id: credential.id, disabled: !credential.disabled },
      {
        onSuccess: (res) => {
          toast.success(res.message)
        },
        onError: (err) => {
          toast.error('操作失败: ' + (err as Error).message)
        },
      }
    )
  }

  const handlePriorityChange = () => {
    const newPriority = parseInt(priorityValue, 10)
    if (isNaN(newPriority) || newPriority < 0) {
      toast.error('优先级必须是非负整数')
      return
    }
    setPriority.mutate(
      { id: credential.id, priority: newPriority },
      {
        onSuccess: (res) => {
          toast.success(res.message)
          setEditingPriority(false)
        },
        onError: (err) => {
          toast.error('操作失败: ' + (err as Error).message)
        },
      }
    )
  }

  const openGroups = () => {
    const ids = joined.map((group) => group.id)
    setGroupDraft(ids.length > 0 ? ids : ['default'])
    setGroupOpen(true)
  }

  const saveGroups = async () => {
    const groups = accessGroups.data ?? []
    const nextIds = groupDraft.length > 0 ? groupDraft : ['default']
    const desired = groups.map((group) => {
      const members = new Set(group.members)
      if (nextIds.includes(group.id)) members.add(credential.id)
      else members.delete(credential.id)
      return { id: group.id, members: [...members] }
    })
    const changed = desired.filter((group) => {
      const previous = groups.find((item) => item.id === group.id)?.members ?? []
      return previous.length !== group.members.length || previous.some((id) => !group.members.includes(id))
    })
    setGroupSaving(true)
    try {
      const ordered = [
        ...changed.filter((group) => nextIds.includes(group.id)),
        ...changed.filter((group) => !nextIds.includes(group.id)),
      ]
      for (const group of ordered) {
        await setGroupMembers(group.id, group.members)
      }
      await queryClient.invalidateQueries({ queryKey: ['access-groups'] })
      setGroupOpen(false)
      toast.success('调度分组已更新')
    } catch (error) {
      toast.error(extractErrorMessage(error))
    } finally {
      setGroupSaving(false)
    }
  }

  const handleReset = () => {
    resetFailure.mutate(credential.id, {
      onSuccess: (res) => {
        toast.success(res.message)
      },
      onError: (err) => {
        toast.error('操作失败: ' + (err as Error).message)
      },
    })
  }

  const handleForceRefresh = () => {
    forceRefresh.mutate(credential.id, {
      onSuccess: (res) => {
        toast.success(res.message)
      },
      onError: (err) => {
        toast.error('刷新失败: ' + (err as Error).message)
      },
    })
  }

  const handleDelete = () => {
    if (!credential.disabled) {
      toast.error('请先禁用凭据再删除')
      setShowDeleteDialog(false)
      return
    }

    deleteCredential.mutate(credential.id, {
      onSuccess: (res) => {
        toast.success(res.message)
        setShowDeleteDialog(false)
      },
      onError: (err) => {
        toast.error('删除失败: ' + (err as Error).message)
      },
    })
  }

  return (
    <>
      <Card className={credential.isCurrent ? 'ring-2 ring-primary' : ''}>
        <CardHeader className="pb-2">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
              <Checkbox
                checked={selected}
                onCheckedChange={onToggleSelect}
              />
              <CardTitle className="truncate text-base">
                {credential.email || `凭据 #${credential.id}`}
              </CardTitle>
              </div>
              <div className="mt-2 flex flex-wrap gap-1">
                {credential.isCurrent && (
                  <Badge variant="success">当前</Badge>
                )}
                {credential.disabled && (
                  <Badge variant="destructive">已禁用</Badge>
                )}
                {!credential.disabled && (credential.cooldownRemainingSeconds ?? 0) > 0 && (
                  <Badge variant="outline">限流冷却 {credential.cooldownRemainingSeconds} 秒</Badge>
                )}
                {credential.disabled && credential.disabledReason && (
                  <Badge variant="outline">{credential.disabledReason}</Badge>
                )}
                {credential.authMethod && (
                  <Badge variant="secondary">
                    {credential.authMethod === 'api_key' ? 'API Key' :
                     credential.authMethod === 'idc' ? 'IdC' :
                     credential.authMethod === 'social' ? 'Social' :
                     credential.authMethod === 'claude_api' ? 'Claude API' :
                     credential.authMethod}
                  </Badge>
                )}
                {credential.authMethod === 'claude_api' && credential.claudeBaseUrl && (
                  <Badge variant="outline">{credential.claudeBaseUrl}</Badge>
                )}
                {credential.endpoint && credential.authMethod !== 'claude_api' && (
                  <Badge variant="outline">{credential.endpoint}</Badge>
                )}
                {(joined.length > 0 ? joined : [{ id: 'default', name: '默认' } as AccessGroup]).map((group) => (
                  <Badge key={group.id} variant="secondary">{group.name}</Badge>
                ))}
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <span className="text-sm text-muted-foreground">启用</span>
              <Switch
                checked={!credential.disabled}
                onCheckedChange={handleToggleDisabled}
                disabled={setDisabled.isPending}
              />
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* 信息网格 */}
          <div className="grid grid-cols-2 gap-4 text-sm">
            <div>
              <span className="text-muted-foreground">优先级：</span>
              {editingPriority ? (
                <div className="inline-flex items-center gap-1 ml-1">
                  <Input
                    type="number"
                    value={priorityValue}
                    onChange={(e) => setPriorityValue(e.target.value)}
                    className="w-16 h-7 text-sm"
                    min="0"
                  />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-7 w-7 p-0"
                    onClick={handlePriorityChange}
                    disabled={setPriority.isPending}
                  >
                    ✓
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-7 w-7 p-0"
                    onClick={() => {
                      setEditingPriority(false)
                      setPriorityValue(String(credential.priority))
                    }}
                  >
                    ✕
                  </Button>
                </div>
              ) : (
                <span
                  className="font-medium cursor-pointer hover:underline ml-1"
                  onClick={() => setEditingPriority(true)}
                >
                  {credential.priority}
                  <span className="text-xs text-muted-foreground ml-1">(点击编辑)</span>
                </span>
              )}
            </div>
            <div>
              <span className="text-muted-foreground">账号成本：</span>
              {editingCost ? (
                <div className="inline-flex items-center gap-1 ml-1">
                  <Input
                    inputMode="decimal"
                    value={costValue}
                    onChange={(e) => setCostValue(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') saveCost()
                      if (e.key === 'Escape') setEditingCost(false)
                    }}
                    placeholder="人民币"
                    aria-label="账号人民币成本"
                    className="w-24 h-7 text-sm"
                    autoFocus
                  />
                  <Button size="sm" variant="ghost" className="h-7 w-7 p-0" onClick={saveCost} disabled={costSaving} aria-label="保存成本">
                    ✓
                  </Button>
                  <Button size="sm" variant="ghost" className="h-7 w-7 p-0" onClick={() => setEditingCost(false)} aria-label="取消">
                    ✕
                  </Button>
                </div>
              ) : (
                <button
                  type="button"
                  className="font-medium underline decoration-dotted underline-offset-4 hover:decoration-solid ml-1"
                  onClick={startEditCost}
                  title="点击编辑。留空表示未知，填 0 表示免费账号"
                >
                  {cost?.costCny == null ? '未填' : formatCNY(cost.costCny)}
                </button>
              )}
            </div>
            <div>
              <span className="text-muted-foreground">总消耗：</span>
              <span className="font-medium">{cost ? `$${cost.usageUsd.toFixed(4)}` : '—'}</span>
            </div>
            <div>
              <span className="text-muted-foreground" title="账号成本 ¥ ÷ 总消耗 $">成本倍率：</span>
              <span className="font-medium">{formatCostRatio(cost?.costRatio)}</span>
            </div>
            {cost && cost.unpricedRequests > 0 ? (
              <div className="col-span-2 text-xs text-muted-foreground">
                有 {cost.unpricedRequests} 条请求的模型没有单价，没算进总消耗
              </div>
            ) : null}
            {credential.maskedApiKey && (
              <div className="col-span-2">
                <span className="text-muted-foreground">API Key：</span>
                <span className="font-mono font-medium">{credential.maskedApiKey}</span>
              </div>
            )}
            {credential.hasProxy && (
              <div className="col-span-2">
                <span className="text-muted-foreground">代理：</span>
                <span className="font-medium">{credential.proxyUrl}</span>
              </div>
            )}
          </div>

          {/* 操作按钮 */}
          <div className="flex flex-wrap gap-2 pt-2 border-t">
            <Button size="sm" variant="outline" onClick={openGroups}>
              调度分组
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={handleReset}
              disabled={resetFailure.isPending || (credential.failureCount === 0 && credential.refreshFailureCount === 0)}
            >
              <RefreshCw className="h-4 w-4 mr-1" />
              重置失败
            </Button>
            {credential.authMethod !== 'claude_api' && (
            <Button
              size="sm"
              variant="outline"
              onClick={handleForceRefresh}
              disabled={forceRefresh.isPending || credential.disabled || credential.authMethod === 'api_key'}
              title={credential.authMethod === 'api_key' ? 'API Key 凭据无需刷新 Token' : credential.disabled ? '已禁用的凭据无法刷新 Token' : '强制刷新 Token'}
            >
              <RefreshCw className={`h-4 w-4 mr-1 ${forceRefresh.isPending ? 'animate-spin' : ''}`} />
              刷新 Token
            </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                const newPriority = Math.max(0, credential.priority - 1)
                setPriority.mutate(
                  { id: credential.id, priority: newPriority },
                  {
                    onSuccess: (res) => toast.success(res.message),
                    onError: (err) => toast.error('操作失败: ' + (err as Error).message),
                  }
                )
              }}
              disabled={setPriority.isPending || credential.priority === 0}
            >
              <ChevronUp className="h-4 w-4 mr-1" />
              提高优先级
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                const newPriority = credential.priority + 1
                setPriority.mutate(
                  { id: credential.id, priority: newPriority },
                  {
                    onSuccess: (res) => toast.success(res.message),
                    onError: (err) => toast.error('操作失败: ' + (err as Error).message),
                  }
                )
              }}
              disabled={setPriority.isPending}
            >
              <ChevronDown className="h-4 w-4 mr-1" />
              降低优先级
            </Button>
            {credential.authMethod !== 'claude_api' && (
            <Button
              size="sm"
              variant="default"
              onClick={() => onViewBalance(credential.id)}
            >
              <Wallet className="h-4 w-4 mr-1" />
              查看余额
            </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              onClick={() => setEditOpen(true)}
            >
              <Pencil className="h-4 w-4 mr-1" />
              编辑
            </Button>
            <Button
              size="sm"
              variant="destructive"
              onClick={() => setShowDeleteDialog(true)}
              disabled={!credential.disabled}
              title={!credential.disabled ? '需要先禁用凭据才能删除' : undefined}
            >
              <Trash2 className="h-4 w-4 mr-1" />
              删除
            </Button>
          </div>
        </CardContent>
      </Card>

      <EditCredentialDialog
        credential={credential}
        open={editOpen}
        onOpenChange={setEditOpen}
      />

      <Dialog open={groupOpen} onOpenChange={setGroupOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>调度分组</DialogTitle>
            <DialogDescription>
              {credential.email || `凭据 #${credential.id}`} 可以同时属于多个分组。移出最后一个分组后，会回到默认组。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            {(accessGroups.data ?? []).map((group) => (
              <label key={group.id} className="flex items-center gap-2 rounded-md border px-3 py-2 text-sm">
                <input
                  type="checkbox"
                  checked={groupDraft.includes(group.id)}
                  onChange={(event) => {
                    setGroupDraft((current) => event.target.checked
                      ? [...current, group.id]
                      : current.filter((id) => id !== group.id))
                  }}
                />
                <span>{group.name}</span>
              </label>
            ))}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setGroupOpen(false)} disabled={groupSaving}>取消</Button>
            <Button onClick={saveGroups} disabled={groupSaving}>保存</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除确认对话框 */}
      <Dialog open={showDeleteDialog} onOpenChange={setShowDeleteDialog}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>确认删除凭据</DialogTitle>
            <DialogDescription>
              您确定要删除凭据 #{credential.id} 吗？此操作无法撤销。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setShowDeleteDialog(false)}
              disabled={deleteCredential.isPending}
            >
              取消
            </Button>
            <Button
              variant="destructive"
              onClick={handleDelete}
              disabled={deleteCredential.isPending || !credential.disabled}
            >
              确认删除
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
