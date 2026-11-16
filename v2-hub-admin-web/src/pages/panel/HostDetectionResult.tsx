import { Descriptions, Tag } from 'antd'
import type { HostDetection } from '../../api/client'
import { DEPLOY_PLAN_LABELS, EMPTY, PANEL_STATE_LABELS, hostErrorText } from './host-labels'


interface HostDetectionResultProps {
  /** 被测的地址（清单第一行）。 */
  address: string
  detection: HostDetection
}

/**
 * 连接测试的结果（docs/03 §5.22 连接测试）：主机密钥指纹、系统、架构、权限、已有面板，
 * 以及部署时会“安装”还是“接管”；不能部署时给出原因大类的说明。结果不保存。
 */
export function HostDetectionResult({ address, detection }: HostDetectionResultProps) {
  const panel = detection.panelVersion === '' ? PANEL_STATE_LABELS[detection.panelState] : `${PANEL_STATE_LABELS[detection.panelState]}（${detection.panelVersion}）`
  return (
    <Descriptions className="form-hint" size="small" column={1} bordered title={`测试结果：${address}`}>
      <Descriptions.Item label="主机密钥指纹">
        <span className="mono">{detection.hostKeySha256 || EMPTY}</span>
      </Descriptions.Item>
      <Descriptions.Item label="系统">{detection.osId || EMPTY}</Descriptions.Item>
      <Descriptions.Item label="架构">{detection.arch || EMPTY}</Descriptions.Item>
      <Descriptions.Item label="权限">
        {detection.isPrivileged ? <Tag color="green">root 或可免密 sudo</Tag> : <Tag color="red">没有 root 权限</Tag>}
      </Descriptions.Item>
      <Descriptions.Item label="已有面板">{panel}</Descriptions.Item>
      <Descriptions.Item label="部署方式">
        {detection.plan === '' ? (
          <>
            <Tag color="red">不能部署</Tag>
            {hostErrorText(detection.reason)}
          </>
        ) : (
          <Tag color="blue">{DEPLOY_PLAN_LABELS[detection.plan]}</Tag>
        )}
      </Descriptions.Item>
    </Descriptions>
  )
}
