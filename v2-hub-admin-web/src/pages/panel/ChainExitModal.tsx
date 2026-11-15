import { Alert, Button, Form, Input, Radio, Segmented, Select, Space, Switch, Typography } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, toApiError, type AdminChainExit, type AdminChainExitInput, type ApiError, type ChainExitAssignMode, type NodeGroup } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useSubmit } from '../../hooks/use-submit'
import { ChainExitFieldsForm } from './ChainExitFieldsForm'
import { ExitUserSelect } from './ExitUserSelect'
import { buildExitLink, emptyExitFields, exitFieldProblems, parseExitLink, type ExitFields } from './chain-exit-link'
import { MAX_NAME_LEN, MAX_REMARK_LEN } from '../form-limits'
import { FormModal } from '../../components/FormModal'

const MAX_LINK_LEN = 2048
const LINK_ROWS = 5
const LABEL_COL = { flex: '150px' }
const MODAL_WIDTH = 720

/** 连接参数的两种填法（docs/05 M9 页面）。 */
type FillMode = 'form' | 'link'
const MODE_FORM: FillMode = 'form'
const MODE_LINK: FillMode = 'link'
const MODE_OPTIONS: { value: FillMode; label: string }[] = [
  { value: MODE_FORM, label: '表单' },
  { value: MODE_LINK, label: '分享链接' },
]

const MSG_UNPARSABLE = '这条链接转不成表单（协议或参数不在表单的范围内），请直接用链接保存。'
const MSG_LINK_REQUIRED = '请粘贴链接'
const MSG_LINK_TOO_LONG = `链接不能超过 ${MAX_LINK_LEN} 个字符`
const CODE_INVALID = 'invalid_argument'

/** 分配方式二选一（Ruling-64）。 */
const ASSIGN_GROUP: ChainExitAssignMode = 'group'
const ASSIGN_USER: ChainExitAssignMode = 'user'
const ASSIGN_OPTIONS: { value: ChainExitAssignMode; label: string }[] = [
  { value: ASSIGN_GROUP, label: '按节点组' },
  { value: ASSIGN_USER, label: '指定用户' },
]

/** antd 表单管的字段：连接参数（表单 / 链接）另由组件状态管。 */
type ExitFormValues = Omit<AdminChainExitInput, 'link'>

function formOf(e: AdminChainExit | null): ExitFormValues {
  if (e === null) {
    return { name: '', remark: '', isEnabled: true, allowServer: true, allowClient: false, assignMode: ASSIGN_GROUP, nodeGroupIds: [], userIds: [] }
  }
  return {
    name: e.name, remark: e.remark, isEnabled: e.isEnabled, allowServer: e.allowServer, allowClient: e.allowClient,
    assignMode: e.assignMode, nodeGroupIds: e.nodeGroups.map((g) => g.id), userIds: e.users.map((u) => u.id),
  }
}

interface ChainExitModalProps {
  /** null = 新建。 */
  exit: AdminChainExit | null
  isOpen: boolean
  groups: NodeGroup[]
  onClose: () => void
  onSaved: () => void
}

/** 新建 / 编辑平台出口的弹窗；每次打开都重建表单（useOpenGeneration）。 */
export function ChainExitModal({ exit, isOpen, groups, onClose, onSaved }: ChainExitModalProps) {
  return (
    <FormModal title={exit === null ? '新建平台出口' : `编辑平台出口：${exit.name}`} isOpen={isOpen} onClose={onClose} width={MODAL_WIDTH}>
      {(generation) => <ExitForm key={generation} exit={exit} groups={groups} onSaved={onSaved} />}
    </FormModal>
  )
}

/**
 * 平台出口表单（docs/05 M9 页面）。连接参数有“表单 / 分享链接”两种填法，可来回切换，提交的始终是一条链接。
 * 编辑时默认不动连接参数；点“修改连接参数”才取一次现有链接（与“查看链接”同一个接口，同样记审计）。
 * 链接与表单内容只留在本组件的内存里，关闭即丢弃。
 */
function ExitForm({ exit, groups, onSaved }: Pick<ChainExitModalProps, 'exit' | 'groups' | 'onSaved'>) {
  const isNew = exit === null
  const [form] = Form.useForm<ExitFormValues>()
  const { error: submitError, isBusy, run } = useSubmit(isNew ? '已创建平台出口' : '已保存', onSaved)
  const [isRelinking, setIsRelinking] = useState(isNew)
  const [mode, setMode] = useState<FillMode>(MODE_FORM)
  const [fields, setFields] = useState<ExitFields>(emptyExitFields)
  const [link, setLink] = useState('')
  const [shouldShowProblems, setShouldShowProblems] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [localError, setLocalError] = useState<ApiError | null>(null)
  const [isLoadingLink, setIsLoadingLink] = useState(false)
  const problems = exitFieldProblems(fields)
  const assignMode = Form.useWatch('assignMode', form) ?? exit?.assignMode ?? ASSIGN_GROUP
  const currentName = () => (form.getFieldValue('name') as string | undefined)?.trim() ?? ''

  /** 把一条链接放进表单；表单表达不了时留在链接方式并说明原因。 */
  const fillFromLink = (text: string) => {
    const parsed = parseExitLink(text)
    if (parsed === null) {
      setMode(MODE_LINK)
      setLink(text)
      setNotice(MSG_UNPARSABLE)
      return
    }
    setFields(parsed.fields)
    setMode(MODE_FORM)
    setNotice(null)
    if (currentName() === '') {
      form.setFieldValue('name', parsed.name)
    }
  }

  const switchMode = (next: FillMode) => {
    setNotice(null)
    if (next === MODE_LINK) {
      // 表单没填完时不覆盖已有的链接文本
      if (problems.size === 0) {
        setLink(buildExitLink(fields, currentName()))
      }
      setMode(MODE_LINK)
      return
    }
    if (link.trim() === '') {
      setMode(MODE_FORM)
      return
    }
    fillFromLink(link.trim())
  }

  /** 编辑时点“修改连接参数”：取一次现有链接，填进表单。 */
  const startRelink = async () => {
    if (exit === null) {
      return
    }
    setIsLoadingLink(true)
    setLocalError(null)
    try {
      const res = await api.GET('/api/admin/chain-exits/{id}/link', { params: { path: { id: exit.id } } })
      if (res.error !== undefined || res.data?.data === undefined) {
        setLocalError(toApiError(res.error))
        return
      }
      fillFromLink(res.data.data.link)
      setIsRelinking(true)
    } catch (err) {
      reportError(err)
      setLocalError(toApiError(err))
    } finally {
      setIsLoadingLink(false)
    }
  }

  /** 这次要提交的链接：undefined = 不改连接参数；null = 还不能提交（缺项已标出或已提示）。 */
  const linkToSubmit = (name: string): string | undefined | null => {
    if (!isRelinking) {
      return undefined
    }
    if (mode === MODE_FORM && problems.size > 0) {
      setShouldShowProblems(true)
      return null
    }
    const text = mode === MODE_FORM ? buildExitLink(fields, name) : link.trim()
    if (text === '' || text.length > MAX_LINK_LEN) {
      setLocalError({ code: CODE_INVALID, message: text === '' ? MSG_LINK_REQUIRED : MSG_LINK_TOO_LONG })
      return null
    }
    return text
  }

  const submit = (v: ExitFormValues) => {
    setLocalError(null)
    const text = linkToSubmit(v.name.trim())
    if (text === null) {
      return
    }
    const common = {
      name: v.name,
      remark: v.remark ?? '',
      isEnabled: v.isEnabled ?? true,
      allowServer: v.allowServer,
      allowClient: v.allowClient,
      // 只提交所选方式那一组，另一组由服务端清空（Ruling-64）
      assignMode: v.assignMode ?? ASSIGN_GROUP,
      nodeGroupIds: v.assignMode === ASSIGN_USER ? [] : (v.nodeGroupIds ?? []),
      userIds: v.assignMode === ASSIGN_USER ? (v.userIds ?? []) : [],
    }
    void run(() =>
      exit === null
        ? api.POST('/api/admin/chain-exits', { params: csrfHeader(), body: { ...common, link: text ?? '' } })
        : // 不带 link 表示不改连接参数
          api.PUT('/api/admin/chain-exits/{id}', { params: { ...csrfHeader(), path: { id: exit.id } }, body: { ...common, link: text } }),
    )
  }

  return (
    <>
      <ErrorAlert error={localError ?? submitError} />
      <Form<ExitFormValues> form={form} layout="horizontal" labelCol={LABEL_COL} labelWrap colon={false} initialValues={formOf(exit)} onFinish={submit}>
        <Form.Item name="name" label="名称" extra="用户在落地下拉里看到的名字" rules={[{ required: true, max: MAX_NAME_LEN, message: `1–${MAX_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        {!isRelinking && (
          <Form.Item label="连接参数">
            <Space>
              <Typography.Text type="secondary">不修改</Typography.Text>
              <Button loading={isLoadingLink} onClick={() => void startRelink()}>
                修改连接参数
              </Button>
            </Space>
          </Form.Item>
        )}
        {isRelinking && (
          <>
            <Form.Item label="填写方式" extra="两种方式可来回切换；保存的始终是一条分享链接，加密存储">
              <Segmented aria-label="填写方式" options={MODE_OPTIONS} value={mode} onChange={switchMode} />
            </Form.Item>
            {notice !== null && <Alert type="info" showIcon className="page-alert" title={notice} />}
            {mode === MODE_FORM ? (
              <ChainExitFieldsForm fields={fields} onChange={setFields} problems={problems} shouldShowProblems={shouldShowProblems} />
            ) : (
              <Form.Item label="链接" htmlFor="chain-exit-link" required extra="分享链接（vless / vmess / trojan / ss），或代理地址 socks5:// http:// https://[用户名:密码@]地址:端口">
                <Input.TextArea
                  id="chain-exit-link"
                  rows={LINK_ROWS}
                  autoComplete="off"
                  spellCheck={false}
                  maxLength={MAX_LINK_LEN}
                  value={link}
                  onChange={(e) => setLink(e.target.value)}
                />
              </Form.Item>
            )}
          </>
        )}
        <Form.Item name="assignMode" label="分配给">
          <Radio.Group optionType="button" options={ASSIGN_OPTIONS} />
        </Form.Item>
        {assignMode === ASSIGN_USER ? (
          <Form.Item name="userIds" label="指定用户" extra="只有选中的用户能用，且其套餐要开了对应的链式；不选 = 没有用户能用">
            <ExitUserSelect initialUsers={exit?.users ?? []} />
          </Form.Item>
        ) : (
          <Form.Item name="nodeGroupIds" label="节点组" extra="套餐绑了其中任一节点组的用户才能用；不选 = 没有用户能用">
            <Select mode="multiple" aria-label="节点组" optionFilterProp="label" options={groups.map((g) => ({ value: g.id, label: g.name }))} />
          </Form.Item>
        )}
        <Form.Item name="allowServer" label="可用于服务端链式" valuePropName="checked" extra="链接只留在起飞面板上，用户看不到">
          <Switch />
        </Form.Item>
        <Form.Item name="allowClient" label="可用于客户端链式" valuePropName="checked" extra="链接会写进用户的订阅，用户拿得走">
          <Switch />
        </Form.Item>
        <Form.Item name="remark" label="备注" rules={[{ max: MAX_REMARK_LEN }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="isEnabled" label="启用" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {isNew ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}
