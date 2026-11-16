import { Alert, App, Button, Descriptions, Drawer, Spin, Table, Typography, type TableProps } from 'antd'
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, csrfHeader, type HostTask, type HostTaskDetail, type HostTaskKind, type ServerHost } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PageHeader } from '../../components/PageHeader'
import { usePollRemote, type Pollable } from '../../hooks/use-polling'
import { mutate, useRemote } from '../../hooks/use-remote'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL, formatTime } from '../system/format'
import { EMPTY, HOST_TASK_KIND_LABELS, hostErrorText, hostStepLabel, isHostTaskActive } from './host-labels'
import { HostTaskStatusTag } from './HostTaskSummary'

const NO_LOG = '（暂无日志）'
const STATUS_QUEUED = 'queued'
const DETAIL_TITLE_LEVEL = 5

interface HostTaskDrawerProps {
  /** 要看任务的主机；null 时抽屉关闭。 */
  host: Pick<ServerHost, 'id' | 'name'> | null
  /** 打开时先选中的任务；不传或为 null 则选最新一条。 */
  initialTaskId?: number | null
  onClose: () => void
  /** 任务结束或被取消后调用，让外面刷新主机列表 / 服务器详情。 */
  onChanged: () => void
}

/**
 * 主机任务抽屉（host:read；docs/03 §5.22 任务、日志与脱敏）：上面是该主机最近的任务，下面是选中任务的步骤与日志。
 * 选中的任务还没结束时每 2 秒刷新一次，结束即停；排队中的任务可取消（host:write）。
 */
export function HostTaskDrawer({ host, initialTaskId, onClose, onChanged }: HostTaskDrawerProps) {
  return (
    <Drawer title={host === null ? '' : `主机任务：${host.name}`} open={host !== null} onClose={onClose} size="large" destroyOnHidden>
      {host !== null && <HostTaskBody key={`${host.id}:${initialTaskId ?? ''}`} hostId={host.id} initialTaskId={initialTaskId ?? null} onChanged={onChanged} />}
    </Drawer>
  )
}

interface HostTaskBodyProps {
  hostId: number
  initialTaskId: number | null
  onChanged: () => void
}

function HostTaskBody({ hostId, initialTaskId, onChanged }: HostTaskBodyProps) {
  const { can } = useSession()
  const loadTasks = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/server-hosts/{id}/tasks', { params: { path: { id: hostId } }, signal }),
    [hostId],
  )
  const tasks = useRemote<HostTask[]>(loadTasks)
  const [selectedId, setSelectedId] = useState<number | null>(initialTaskId)
  // 没有指定时看最新一条（列表新的在前）
  const currentId = selectedId ?? tasks.data?.[0]?.id ?? null

  const columns: TableProps<HostTask>['columns'] = [
    { title: '任务', dataIndex: 'kind', render: (kind: HostTaskKind) => HOST_TASK_KIND_LABELS[kind] ?? kind },
    { title: '状态', key: 'status', render: (_: unknown, t: HostTask) => <HostTaskStatusTag status={t.status} /> },
    { title: '发起人', dataIndex: 'createdByName', render: (v: string) => v || EMPTY },
    { title: '创建时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
  ]

  return (
    <>
      <ErrorAlert error={tasks.error} />
      <Table<HostTask>
        rowKey="id"
        size="small"
        className="page-alert"
        scroll={TABLE_SCROLL}
        // 轮询刷新时不闪加载态
        loading={tasks.isLoading && tasks.data === null}
        columns={columns}
        dataSource={tasks.data ?? []}
        pagination={false}
        rowSelection={{
          type: 'radio',
          selectedRowKeys: currentId === null ? [] : [currentId],
          onChange: (keys) => setSelectedId(Number(keys[0])),
        }}
        onRow={(t) => ({ onClick: () => setSelectedId(t.id) })}
      />
      {currentId !== null && (
        <HostTaskPanel
          key={currentId}
          taskId={currentId}
          canWrite={can(PERMISSIONS.HOST_WRITE)}
          tasks={tasks}
          onChanged={() => {
            tasks.reload()
            onChanged()
          }}
        />
      )}
    </>
  )
}

interface HostTaskPanelProps {
  taskId: number
  canWrite: boolean
  /** 任务列表：这条任务没结束时跟着详情一起定时刷新。 */
  tasks: Pollable
  /** 任务从未结束变成结束（含被取消）时调用。 */
  onChanged: () => void
}

/** 一条任务的详情与日志。日志是后端脱敏后的纯文本，放在 pre 里原样显示。 */
function HostTaskPanel({ taskId, canWrite, tasks, onChanged }: HostTaskPanelProps) {
  const { message } = App.useApp()
  const [isCanceling, setIsCanceling] = useState(false)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/admin/host-tasks/{id}', { params: { path: { id: taskId } }, signal }), [taskId])
  const detail = useRemote<HostTaskDetail>(load)
  const task = detail.data
  const isActive = isHostTaskActive(task)

  usePollRemote([detail, tasks], isActive)

  const wasActive = useRef(false)
  useEffect(() => {
    if (wasActive.current && !isActive) {
      onChanged()
    }
    wasActive.current = isActive
  }, [isActive, onChanged])

  const handleCancel = async () => {
    setIsCanceling(true)
    const err = await mutate(() => api.POST('/api/admin/host-tasks/{id}/cancel', { params: { ...csrfHeader(), path: { id: taskId } } }))
    setIsCanceling(false)
    if (err === null) {
      void message.success('已取消')
    } else {
      // 多半是任务已经开始（409）：照样刷新，显示它现在的状态
      void message.error(err.message)
    }
    detail.reload()
  }

  if (task === null) {
    return detail.error === null ? <Spin /> : <ErrorAlert error={detail.error} />
  }
  const errorText = hostErrorText(task.errorCode)
  const hasFailure = errorText !== '' || task.lastError !== ''
  const canCancel = canWrite && task.status === STATUS_QUEUED
  return (
    <>
      <PageHeader
        title={`${HOST_TASK_KIND_LABELS[task.kind] ?? task.kind} #${task.id}`}
        level={DETAIL_TITLE_LEVEL}
        action={canCancel && (
          <Button danger loading={isCanceling} onClick={() => void handleCancel()}>
            取消任务
          </Button>
        )}
      />
      <ErrorAlert error={detail.error} />
      <Descriptions size="small" column={{ xs: 1, md: 2 }} className="page-alert">
        <Descriptions.Item label="状态">
          <HostTaskStatusTag status={task.status} />
        </Descriptions.Item>
        <Descriptions.Item label="步骤">{hostStepLabel(task.step)}</Descriptions.Item>
        <Descriptions.Item label="发起人">{task.createdByName || EMPTY}</Descriptions.Item>
        <Descriptions.Item label="创建时间">{formatTime(task.createdAt)}</Descriptions.Item>
        <Descriptions.Item label="开始时间">{formatTime(task.startedAt)}</Descriptions.Item>
        <Descriptions.Item label="结束时间">{formatTime(task.finishedAt)}</Descriptions.Item>
      </Descriptions>
      {hasFailure && (
        <Alert
          type="error"
          showIcon
          className="page-alert"
          title={errorText === '' ? task.lastError : errorText}
          description={errorText !== '' && task.lastError !== '' ? task.lastError : undefined}
        />
      )}
      <Typography.Text strong>日志</Typography.Text>
      <pre className="host-task-log" aria-label="任务日志">
        {task.log === '' ? NO_LOG : task.log}
      </pre>
    </>
  )
}
