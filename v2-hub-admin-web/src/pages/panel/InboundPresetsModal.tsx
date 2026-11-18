import { Alert, App, Button, Checkbox, InputNumber, Select, Space, Table, Typography } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, toApiError, type ApiError, type HostTask, type InboundPresetKey, type InboundPresetPreview, type NodeGroup } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { useRemote } from '../../hooks/use-remote'
import { MAX_PORT, MIN_PORT } from '../form-limits'
import { INBOUND_PRESET_LABELS, VISION_PRESETS } from './inbound-presets'
import { NodeAffixInputs } from './NodeAffixInputs'
import { EMPTY_NODE_AFFIX, affixName, isAffixEmpty, normalizeAffix, type NodeAffix } from './node-affix'

const MODAL_WIDTH = 760
/** 前后缀预览里用的示例默认名（第一个预设的入站备注）。 */
const SAMPLE_NODE_NAME = 'VLESS-REALITY'

interface InboundPresetsModalProps {
  serverId: number
  isOpen: boolean
  onClose: () => void
  /** 任务排上之后调用：参数是排队中的任务（自带所在主机的 hostId）。 */
  onStarted: (task: HostTask) => void
}

/**
 * 服务器详情页的“一键部署入站”弹窗（docs/03 §5.1，Ruling-76；node:write 且 host:write，由调用方判断）。
 * 打开时先拿预览：7 个预设能否部署与建议端口；默认勾选全部可用的，端口可改，可选节点组与节点显示名的前后缀（Ruling-80）；提交后排一条主机任务。
 */
export function InboundPresetsModal({ serverId, isOpen, onClose, onStarted }: InboundPresetsModalProps) {
  return (
    <FormModal title="一键部署入站" isOpen={isOpen} onClose={onClose} width={MODAL_WIDTH}>
      {(generation) => <InboundPresetsBody key={generation} serverId={serverId} onStarted={onStarted} />}
    </FormModal>
  )
}

type PresetRow = InboundPresetPreview['items'][number]

const loadGroups = (signal: AbortSignal) => api.GET('/api/admin/node-groups', { signal })

function InboundPresetsBody({ serverId, onStarted }: Pick<InboundPresetsModalProps, 'serverId' | 'onStarted'>) {
  const { message } = App.useApp()
  const loadPreview = useCallback(
    (signal: AbortSignal) => api.POST('/api/admin/servers/{id}/inbound-presets/preview', { params: { ...csrfHeader(), path: { id: serverId } }, signal }),
    [serverId],
  )
  const preview = useRemote<InboundPresetPreview>(loadPreview)
  const groups = useRemote<NodeGroup[]>(loadGroups)
  // 勾选与端口：没改过的用预览给的（全部可用的默认勾上）
  const [picked, setPicked] = useState<Partial<Record<InboundPresetKey, boolean>>>({})
  const [ports, setPorts] = useState<Partial<Record<InboundPresetKey, number>>>({})
  const [groupIds, setGroupIds] = useState<number[]>([])
  const [affix, setAffix] = useState<NodeAffix>(EMPTY_NODE_AFFIX)
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const data = preview.data
  const rows: PresetRow[] = data?.items ?? []
  const isPicked = (r: PresetRow) => r.available && (picked[r.key] ?? true)
  const portOf = (r: PresetRow) => ports[r.key] ?? r.port
  const chosen = rows.filter(isPicked)
  const needsCertFirst = data !== null && data.domain !== '' && !data.certReady && chosen.some((r) => r.needsCert)
  const turnsOnVision = chosen.some((r) => VISION_PRESETS.includes(r.key))

  const handleSubmit = async () => {
    const items = chosen.map((r) => ({ key: r.key, port: portOf(r) }))
    const { prefix, suffix } = normalizeAffix(affix)
    setIsBusy(true)
    setError(null)
    try {
      const { data: res, error: apiError } = await api.POST('/api/admin/servers/{id}/inbound-presets', {
        params: { ...csrfHeader(), path: { id: serverId } },
        body: { items, nodeGroupIds: groupIds, namePrefix: prefix, nameSuffix: suffix },
      })
      if (apiError !== undefined || res?.data === undefined) {
        setError(toApiError(apiError))
        return
      }
      void message.success('已开始部署')
      onStarted(res.data)
    } catch (err) {
      reportError(err)
      setError(toApiError(err))
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <>
      <ErrorAlert error={preview.error ?? error} />
      {needsCertFirst && (
        <Alert type="info" showIcon className="page-alert" title={`主机 ${data.domain} 还没有证书，部署时会先申请（一两分钟），再建需要 TLS 的入站`} />
      )}
      <Table<PresetRow>
        rowKey="key"
        size="small"
        loading={preview.isLoading}
        dataSource={rows}
        pagination={false}
        columns={[
          {
            title: '预设',
            render: (_, r) => (
              <Checkbox checked={isPicked(r)} disabled={!r.available} onChange={(e) => setPicked({ ...picked, [r.key]: e.target.checked })}>
                <Space orientation="vertical" size={0}>
                  <span>{INBOUND_PRESET_LABELS[r.key].name}</span>
                  <Typography.Text type="secondary">{r.available ? INBOUND_PRESET_LABELS[r.key].detail : r.reason}</Typography.Text>
                </Space>
              </Checkbox>
            ),
          },
          {
            title: '端口',
            width: 140,
            render: (_, r) => (
              <InputNumber
                aria-label={`${INBOUND_PRESET_LABELS[r.key].name} 端口`}
                min={MIN_PORT}
                max={MAX_PORT}
                precision={0}
                value={portOf(r)}
                disabled={!isPicked(r)}
                onChange={(v) => setPorts({ ...ports, [r.key]: v ?? undefined })}
              />
            ),
          },
        ]}
      />
      <Typography.Paragraph type="secondary" className="form-hint">
        端口随机取自 10000–60000，已避开主机上正在监听的端口与面板已有入站；主机开着 ufw / firewalld 时会自动放行。
        {turnsOnVision && ' 建了 REALITY 入站后会打开这台服务器的 VLESS 流控（Vision）。'}
      </Typography.Paragraph>
      <Space orientation="vertical" className="full-width">
        <Select<number[]>
          mode="multiple"
          allowClear
          className="full-width"
          placeholder="把新节点加进节点组（可不选）"
          loading={groups.isLoading}
          value={groupIds}
          onChange={setGroupIds}
          options={(groups.data ?? []).map((g) => ({ value: g.id, label: g.name }))}
        />
        <NodeAffixInputs value={affix} onChange={setAffix} />
        <Typography.Text type="secondary">
          {isAffixEmpty(affix)
            ? '节点显示名默认取预设名；填了前缀 / 后缀会加在两头（可不填）。'
            : `节点显示名形如「${affixName(SAMPLE_NODE_NAME, affix)}」；面板上的入站备注不变。`}
        </Typography.Text>
        <Button type="primary" loading={isBusy} disabled={data === null || chosen.length === 0} onClick={() => void handleSubmit()}>
          部署 {chosen.length} 个入站
        </Button>
      </Space>
    </>
  )
}
