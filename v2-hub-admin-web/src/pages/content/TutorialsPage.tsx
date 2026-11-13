import { MinusCircleOutlined, PlusOutlined } from '@ant-design/icons'
import { Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Typography, type TableProps } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, type Tutorial, type TutorialInput, type TutorialPlatform } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction, useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL } from '../system/format'
import { MARKDOWN_HINT, MAX_MARKDOWN_LEN } from './markdown'

const MAX_CLIENT_NAME_LEN = 64
const MAX_SUMMARY_LEN = 255
const MAX_LINKS = 5
const MAX_LINK_LABEL_LEN = 32
const MAX_LINK_URL_LEN = 512
const MAX_SORT_ORDER = 100000
const CONTENT_ROWS = 12
const MODAL_WIDTH = 720
// 下载链接只收 https（docs/03 §5.9）
const HTTPS_URL_PATTERN = /^https:\/\//

/** 平台的显示名；取值见 hub-api.yaml 的 TutorialPlatform。 */
const PLATFORM_LABELS: Record<TutorialPlatform, string> = {
  windows: 'Windows',
  macos: 'macOS',
  android: 'Android',
  ios: 'iOS',
  linux: 'Linux',
  router: '路由器',
}
const PLATFORM_OPTIONS = (Object.keys(PLATFORM_LABELS) as TutorialPlatform[]).map((value) => ({ value, label: PLATFORM_LABELS[value] }))

const loadTutorials = (signal: AbortSignal) => api.GET('/api/admin/tutorials', { signal })

function formOf(t: Tutorial | null): TutorialInput {
  if (t === null) {
    return { platform: 'windows', clientName: '', summary: '', links: [], content: '', sortOrder: 0, isPublished: true }
  }
  return { platform: t.platform, clientName: t.clientName, summary: t.summary, links: t.links, content: t.content, sortOrder: t.sortOrder, isPublished: t.isPublished }
}

/** 教程与客户端下载（content:read 查看，content:write 增改删；docs/03 §5.9）。 */
export function TutorialsPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.CONTENT_WRITE)
  const tutorials = useRemote<Tutorial[]>(loadTutorials)
  const act = useRowAction(tutorials.reload)
  const [editing, setEditing] = useState<Tutorial | null>(null)
  const [isOpen, setIsOpen] = useState(false)

  const open = (t: Tutorial | null) => {
    setEditing(t)
    setIsOpen(true)
  }

  const columns: TableProps<Tutorial>['columns'] = [
    { title: '平台', dataIndex: 'platform', render: (p: TutorialPlatform) => <Tag color="blue">{PLATFORM_LABELS[p] ?? p}</Tag> },
    { title: '客户端', dataIndex: 'clientName' },
    { title: '简介', dataIndex: 'summary', render: (v: string) => v || '—' },
    { title: '下载链接', dataIndex: 'links', render: (links: Tutorial['links']) => `${links.length} 个` },
    { title: '排序', dataIndex: 'sortOrder' },
    { title: '状态', dataIndex: 'isPublished', render: (isPublished: boolean) => (isPublished ? <Tag color="green">已发布</Tag> : <Tag>未发布</Tag>) },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, t: Tutorial) => (
        <Space>
          <Button size="small" onClick={() => open(t)}>
            编辑
          </Button>
          <Popconfirm
            title={`删除教程 ${t.clientName}？`}
            onConfirm={() => act(() => api.DELETE('/api/admin/tutorials/{id}', { params: { ...csrfHeader(), path: { id: t.id } } }), '已删除')}
          >
            <Button size="small" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    })
  }

  return (
    <>
      <Space className="page-header">
        <Typography.Title level={3}>教程</Typography.Title>
        {canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open(null)}>
            新建教程
          </Button>
        )}
      </Space>
      <ErrorAlert error={tutorials.error} />
      <Table<Tutorial> rowKey="id" scroll={TABLE_SCROLL} loading={tutorials.isLoading} columns={columns} dataSource={tutorials.data ?? []} pagination={false} />
      <TutorialModal
        tutorial={editing}
        isOpen={isOpen}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          tutorials.reload()
        }}
      />
    </>
  )
}

interface TutorialModalProps {
  /** null = 新建。 */
  tutorial: Tutorial | null
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

function TutorialModal({ tutorial, isOpen, onClose, onSaved }: TutorialModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title={tutorial === null ? '新建教程' : `编辑教程：${tutorial.clientName}`} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={MODAL_WIDTH}>
      <TutorialForm key={generation} tutorial={tutorial} onSaved={onSaved} />
    </Modal>
  )
}

function TutorialForm({ tutorial, onSaved }: Pick<TutorialModalProps, 'tutorial' | 'onSaved'>) {
  const { error, isBusy, run } = useSubmit(tutorial === null ? '已创建教程' : '已保存', onSaved)
  const submit = (v: TutorialInput) => {
    const body: TutorialInput = {
      platform: v.platform,
      clientName: v.clientName,
      summary: v.summary ?? '',
      links: v.links ?? [],
      content: v.content ?? '',
      sortOrder: v.sortOrder ?? 0,
      isPublished: v.isPublished,
    }
    return run(() =>
      tutorial === null
        ? api.POST('/api/admin/tutorials', { params: csrfHeader(), body })
        : api.PUT('/api/admin/tutorials/{id}', { params: { ...csrfHeader(), path: { id: tutorial.id } }, body }),
    )
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<TutorialInput> layout="vertical" initialValues={formOf(tutorial)} onFinish={(v) => void submit(v)}>
        <Form.Item name="platform" label="平台" rules={[{ required: true, message: '请选择平台' }]}>
          <Select options={PLATFORM_OPTIONS} />
        </Form.Item>
        <Form.Item name="clientName" label="客户端名称" rules={[{ required: true, max: MAX_CLIENT_NAME_LEN, message: `1–${MAX_CLIENT_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="summary" label="简介" rules={[{ max: MAX_SUMMARY_LEN }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item label="下载链接" extra={`最多 ${MAX_LINKS} 个，只收 https 地址`}>
          <Form.List name="links">
            {(fields, { add, remove }) => (
              <Space orientation="vertical" className="full-width">
                {fields.map((field) => (
                  <Space key={field.key} align="baseline">
                    <Form.Item name={[field.name, 'label']} noStyle rules={[{ required: true, max: MAX_LINK_LABEL_LEN, message: '请填按钮文字' }]}>
                      <Input autoComplete="off" placeholder="按钮文字" aria-label="按钮文字" />
                    </Form.Item>
                    <Form.Item
                      name={[field.name, 'url']}
                      noStyle
                      rules={[{ required: true, max: MAX_LINK_URL_LEN, pattern: HTTPS_URL_PATTERN, message: '请填 https 地址' }]}
                    >
                      <Input autoComplete="off" placeholder="https://" aria-label="下载地址" className="link-url-input" />
                    </Form.Item>
                    <Button type="text" icon={<MinusCircleOutlined />} aria-label="移除链接" onClick={() => remove(field.name)} />
                  </Space>
                ))}
                {fields.length < MAX_LINKS && (
                  <Button type="dashed" icon={<PlusOutlined />} onClick={() => add({ label: '', url: '' })}>
                    添加链接
                  </Button>
                )}
              </Space>
            )}
          </Form.List>
        </Form.Item>
        <Form.Item name="content" label="使用说明" extra={MARKDOWN_HINT} rules={[{ max: MAX_MARKDOWN_LEN }]}>
          <Input.TextArea rows={CONTENT_ROWS} className="mono" showCount maxLength={MAX_MARKDOWN_LEN} />
        </Form.Item>
        <Form.Item name="sortOrder" label="排序" extra="小的在前">
          <InputNumber min={0} max={MAX_SORT_ORDER} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="isPublished" label="发布" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {tutorial === null ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}
