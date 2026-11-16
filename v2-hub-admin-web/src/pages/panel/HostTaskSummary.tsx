import { Button, Space, Tag, Typography } from 'antd'
import type { HostTask } from '../../api/client'
import { HOST_TASK_KIND_LABELS, HOST_TASK_STATUS_META, hostErrorText } from './host-labels'

const EMPTY = '—'
const STATUS_FAILED = 'failed'

/** 任务状态标签。 */
export function HostTaskStatusTag({ status }: { status: HostTask['status'] }) {
  const meta = HOST_TASK_STATUS_META[status]
  return <Tag color={meta?.color}>{meta?.label ?? status}</Tag>
}

interface HostTaskSummaryProps {
  /** 主机最近一条任务；没有为 null。 */
  task: HostTask | null
  /** 点任务名时调用（打开任务抽屉并选中这条）。 */
  onOpen: (task: HostTask) => void
}

/** 主机最近一条任务的概况：种类（可点开看步骤与日志）+ 状态标签；失败时带上原因大类的说明。 */
export function HostTaskSummary({ task, onOpen }: HostTaskSummaryProps) {
  if (task === null) {
    return <>{EMPTY}</>
  }
  const errorText = task.status === STATUS_FAILED ? hostErrorText(task.errorCode) : ''
  return (
    <Space orientation="vertical" size={0}>
      <Space size={4}>
        <Button size="small" type="link" className="link-button" onClick={() => onOpen(task)}>
          {HOST_TASK_KIND_LABELS[task.kind] ?? task.kind}
        </Button>
        <HostTaskStatusTag status={task.status} />
      </Space>
      {errorText !== '' && (
        <Typography.Text type="danger" className="host-task-error">
          {errorText}
        </Typography.Text>
      )}
    </Space>
  )
}
