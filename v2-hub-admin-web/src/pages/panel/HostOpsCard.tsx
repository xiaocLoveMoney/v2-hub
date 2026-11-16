import { Button, Descriptions, Typography } from 'antd'
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { api, type HostTask, type Server, type ServerHost } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PageHeader } from '../../components/PageHeader'
import { usePolling } from '../../hooks/use-polling'
import { useRemote } from '../../hooks/use-remote'
import { PERMISSIONS } from '../../menu'
import { HostBindModal } from './HostBindModal'
import { HostOpsActions } from './HostOpsActions'
import { HostTaskDrawer } from './HostTaskDrawer'
import { HostTaskSummary } from './HostTaskSummary'
import type { OpenedHostTasks } from './host-actions'
import { isHostTaskActive, isPlainHttpUrl } from './host-labels'
import { formatHostEndpoint } from './host-list'

const CARD_TITLE = 'SSH 运维'
const CARD_TITLE_LEVEL = 4
/** 没绑定主机时占位用的 ID；这时不会发请求。 */
const NO_HOST_ID = 0

interface HostOpsCardProps {
  server: Server
  /** 绑定成功或任务结束后调用，让详情页重新拉服务器（地址、状态、hostId 可能变了）。 */
  onChanged: () => void
}

/**
 * 服务器详情页里的 SSH 运维（docs/03 §5.22；host:read 才渲染，由调用方判断）。
 * 没绑定主机：说明 + “绑定 SSH”（host:write）。已绑定：主机地址、凭据、最近任务与运维动作（host:write）；
 * 发起动作后打开任务抽屉看进度，任务结束后通知详情页刷新。
 */
export function HostOpsCard({ server, onChanged }: HostOpsCardProps) {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.HOST_WRITE)
  const hostId = server.hostId ?? null
  const [isBinding, setIsBinding] = useState(false)
  const [opened, setOpened] = useState<OpenedHostTasks | null>(null)
  const loadHost = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/server-hosts/{id}', { params: { path: { id: hostId ?? NO_HOST_ID } }, signal }),
    [hostId],
  )
  const host = useRemote<ServerHost>(loadHost, hostId !== null)
  const current = host.data
  const lastTask = current?.lastTask ?? null

  // 任务没结束时每 2 秒刷新主机，结束即停
  const isTaskActive = isHostTaskActive(lastTask)
  usePolling(() => {
    if (!host.isLoading) {
      host.reload()
    }
  }, isTaskActive)

  // 盯着一条任务（刚发起的，或看到它还没结束的）：等它结束时通知详情页刷新一次，服务器的地址、状态可能跟着变了。
  // 按任务 ID 记而不是按“是否进行中”的变化记：任务很快结束、这里没来得及看到它进行中时也不会漏掉。
  const watchedTaskId = useRef<number | null>(null)
  useEffect(() => {
    if (lastTask === null) {
      return
    }
    if (isHostTaskActive(lastTask)) {
      watchedTaskId.current = lastTask.id
      return
    }
    if (watchedTaskId.current === lastTask.id) {
      watchedTaskId.current = null
      onChanged()
    }
  })

  const openTasks = (target: Pick<ServerHost, 'id' | 'name'>, task: HostTask | null) =>
    setOpened({ host: { id: target.id, name: target.name }, taskId: task?.id ?? null })

  /** 发起了一条新任务：打开抽屉看进度，并盯着它结束。 */
  const watchTask = (target: Pick<ServerHost, 'id' | 'name'>, task: HostTask | null) => {
    watchedTaskId.current = task?.id ?? null
    openTasks(target, task)
  }

  const handleBound = (bound: ServerHost) => {
    setIsBinding(false)
    watchTask(bound, bound.lastTask)
    // 服务器现在有 hostId 了，让详情页重新拉一次
    onChanged()
  }

  let body: ReactNode
  if (hostId === null) {
    body = (
      <>
        <PageHeader title={CARD_TITLE} level={CARD_TITLE_LEVEL} action={canWrite && <Button onClick={() => setIsBinding(true)}>绑定 SSH</Button>} />
        <Typography.Paragraph type="secondary">
          这台服务器还没有绑定 SSH。绑定后可以在这里重启面板、升级或修复、重签令牌、配置证书、查看状态与日志。
        </Typography.Paragraph>
        <HostBindModal server={server} isOpen={isBinding} onClose={() => setIsBinding(false)} onBound={handleBound} />
      </>
    )
  } else {
    body = (
      <>
        <PageHeader
          title={CARD_TITLE}
          level={CARD_TITLE_LEVEL}
          action={current !== null && <Button onClick={() => openTasks(current, null)}>查看任务</Button>}
        />
        <ErrorAlert error={host.error} />
        {current !== null && (
          <Descriptions size="small" column={{ xs: 1, md: 3 }} className="page-alert">
            <Descriptions.Item label="主机地址">
              <span className="mono">{formatHostEndpoint(current.address, current.sshPort)}</span>
            </Descriptions.Item>
            <Descriptions.Item label="凭据">{current.credentialName}</Descriptions.Item>
            <Descriptions.Item label="最近任务">
              <HostTaskSummary task={lastTask} onOpen={(task) => openTasks(current, task)} />
            </Descriptions.Item>
          </Descriptions>
        )}
        {current !== null && canWrite && (
          <HostOpsActions
            hostId={current.id}
            isPlainHttp={isPlainHttpUrl(server.baseUrl)}
            isBusy={isTaskActive}
            onStarted={(task) => {
              watchTask(current, task)
              host.reload()
            }}
          />
        )}
      </>
    )
  }

  return (
    <>
      {body}
      {/* 抽屉放在两种状态之外：绑定成功后卡片从“未绑定”切到“已绑定”，正在看的任务不受影响 */}
      <HostTaskDrawer
        host={opened?.host ?? null}
        initialTaskId={opened?.taskId}
        onClose={() => {
          setOpened(null)
          host.reload()
        }}
        onChanged={host.reload}
      />
    </>
  )
}
