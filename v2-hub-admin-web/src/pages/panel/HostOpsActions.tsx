import { Alert, App, Button, Descriptions, Modal, Popconfirm, Space, Typography } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, toApiError, type HostAction, type HostTask, type PanelLogin } from '../../api/client'
import { startHostAction } from './host-actions'

/** 一个运维动作的按钮与二次确认文案；description 写清影响（docs/03 §5.22 运维动作）。 */
interface OpsAction {
  action: HostAction
  label: string
  title: string
  description: string
}

const ACTION_ENABLE_TLS: HostAction = 'enable_tls'

const OPS_ACTIONS: readonly OpsAction[] = [
  { action: 'restart_panel', label: '重启面板', title: '重启面板？', description: '会连带重启内核，在线连接会断一下' },
  { action: 'restart_core', label: '重启内核', title: '重启内核？', description: '只让面板重载内核，面板本身不重启' },
  {
    action: 'upgrade',
    label: '升级 / 修复',
    title: '升级或修复面板？',
    description: '升到平台基线版本，数据与设置保留；版本已是基线时相当于修复程序文件',
  },
  {
    action: 'reissue_token',
    label: '重签令牌',
    title: '重签令牌？',
    description: '重新签发面板上名为 v2-hub 的令牌并更新这台服务器的接入信息，原来的同名令牌作废；用于面板状态是“token 失效”时',
  },
  { action: 'diagnose', label: '查看状态与日志', title: '查看状态与最近日志？', description: '只读取面板的状态与最近日志，不改动机器；结果在任务日志里看' },
  {
    action: ACTION_ENABLE_TLS,
    label: '配置证书',
    title: '给面板配置证书？',
    description: '生成自签证书设给面板，并把这台服务器改成 https + 固定证书指纹接入，面板令牌不再明文传输',
  },
]

interface HostOpsActionsProps {
  hostId: number
  /** 面板地址是明文 HTTP 时才出现“配置证书”。 */
  isPlainHttp: boolean
  /** 主机有未结束的任务：按钮不可点（再发起后端会回 409）。 */
  isBusy: boolean
  /** 动作已排成任务后调用（打开任务抽屉看进度）。 */
  onStarted: (task: HostTask) => void
}

/**
 * 已绑定 SSH 的服务器的运维动作（都要 host:write，由调用方判断后才渲染）：每个动作二次确认并写清影响，
 * 发起后排成一条任务；“重置面板登录口令”直接执行，结果只在弹窗里显示一次（docs/03 §5.22 运维动作、重置面板登录口令）。
 */
export function HostOpsActions({ hostId, isPlainHttp, isBusy, onStarted }: HostOpsActionsProps) {
  const { message } = App.useApp()
  const [isResetting, setIsResetting] = useState(false)
  // 新的面板登录信息只放在这个组件的内存里：关掉弹窗即丢，不进任何存储
  const [panelLogin, setPanelLogin] = useState<PanelLogin | null>(null)
  const actions = OPS_ACTIONS.filter((a) => a.action !== ACTION_ENABLE_TLS || isPlainHttp)

  const handleAction = async (action: HostAction) => {
    const result = await startHostAction(hostId, action)
    if (result.error !== null || result.task === null) {
      void message.error(toApiError(result.error).message)
      return
    }
    onStarted(result.task)
  }

  const handleResetPanelLogin = async () => {
    setIsResetting(true)
    try {
      const { data, error } = await api.POST('/api/admin/server-hosts/{id}/panel-login/reset', { params: { ...csrfHeader(), path: { id: hostId } } })
      if (error !== undefined || data?.data === undefined) {
        void message.error(toApiError(error).message)
        return
      }
      setPanelLogin(data.data)
    } catch (err) {
      reportError(err)
      void message.error(toApiError(err).message)
    } finally {
      setIsResetting(false)
    }
  }

  const closePanelLogin = () => setPanelLogin(null)

  return (
    <>
      <Space wrap className="host-ops-actions">
        {actions.map((a) => (
          <Popconfirm key={a.action} title={a.title} description={a.description} onConfirm={() => handleAction(a.action)}>
            <Button disabled={isBusy}>{a.label}</Button>
          </Popconfirm>
        ))}
        <Popconfirm
          title="重置面板登录口令？"
          description="平台生成随机的用户名与口令设给面板，原来的面板登录信息作废；面板的两步验证不动。结果只显示一次"
          onConfirm={handleResetPanelLogin}
        >
          <Button danger disabled={isBusy} loading={isResetting}>
            重置面板登录口令
          </Button>
        </Popconfirm>
      </Space>
      {/* 有结果才挂载弹窗：关闭时整个卸载，口令不会随关闭动画多留在页面里 */}
      {panelLogin !== null && (
        <Modal
          title="新的面板登录信息"
          open
          onCancel={closePanelLogin}
          footer={
            <Button type="primary" onClick={closePanelLogin}>
              我已保存
            </Button>
          }
        >
          <Alert type="warning" showIcon className="page-alert" title="只显示这一次：关闭后无法再查看，平台不保存。" />
          <Descriptions size="small" column={1} bordered>
            <Descriptions.Item label="用户名">
              <Typography.Text copyable className="mono">
                {panelLogin.username}
              </Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label="口令">
              <Typography.Text copyable className="mono">
                {panelLogin.password}
              </Typography.Text>
            </Descriptions.Item>
          </Descriptions>
        </Modal>
      )}
    </>
  )
}
