import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Form, Input, Modal, Popconfirm, Space, Switch, Table, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type Announcement, type AnnouncementInput, type PageData } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { mutate, useRemote } from '../../hooks/use-remote'
import { useRowAction, useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL, formatTime } from '../system/format'
import { MARKDOWN_HINT, MAX_MARKDOWN_LEN } from './markdown'

const DEFAULT_PAGE_SIZE = 20
const MAX_TITLE_LEN = 100
const CONTENT_ROWS = 14
const MODAL_WIDTH = 720

function formOf(a: Announcement | null): AnnouncementInput {
  if (a === null) {
    return { title: '', content: '', isPinned: false, isPopup: false, isPublished: true }
  }
  return { title: a.title, content: a.content, isPinned: a.isPinned, isPopup: a.isPopup, isPublished: a.publishedAt !== null }
}

/** 公告（content:read 查看，content:write 增改删与群发邮件；docs/03 §5.9）。 */
export function AnnouncementsPage() {
  const { can } = useSession()
  const { message } = App.useApp()
  const canWrite = can(PERMISSIONS.CONTENT_WRITE)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE)
  const [editing, setEditing] = useState<Announcement | null>(null)
  const [isOpen, setIsOpen] = useState(false)
  const loadAnnouncements = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/announcements', { params: { query: { page, pageSize } }, signal }),
    [page, pageSize],
  )
  const announcements = useRemote<PageData<Announcement>>(loadAnnouncements)
  const act = useRowAction(announcements.reload)

  const open = (a: Announcement | null) => {
    setEditing(a)
    setIsOpen(true)
  }

  const sendMail = async (a: Announcement) => {
    let queued = 0
    const err = await mutate(async () => {
      const res = await api.POST('/api/admin/announcements/{id}/mail', { params: { ...csrfHeader(), path: { id: a.id } } })
      queued = res.data?.data?.queued ?? 0
      return res
    })
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success(`已入发件箱 ${queued} 封`)
    announcements.reload()
  }

  const columns: TableProps<Announcement>['columns'] = [
    {
      title: '标题',
      dataIndex: 'title',
      render: (title: string, a: Announcement) => (
        <Space size={4}>
          {title}
          {a.isPinned && <Tag color="gold">置顶</Tag>}
          {a.isPopup && <Tag color="purple">弹窗</Tag>}
        </Space>
      ),
    },
    {
      title: '状态',
      dataIndex: 'publishedAt',
      render: (v: string | null) => (v === null ? <Tag>草稿</Tag> : <Tag color="green">已发布</Tag>),
    },
    { title: '发布时间', key: 'publishedTime', render: (_: unknown, a: Announcement) => formatTime(a.publishedAt) },
    { title: '群发邮件', dataIndex: 'mailedAt', render: (v: string | null | undefined) => (v === null || v === undefined ? '未发' : formatTime(v)) },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, a: Announcement) => {
        // 每条公告只能群发一次，且须已发布
        const canMail = a.publishedAt !== null && (a.mailedAt === null || a.mailedAt === undefined)
        return (
          <Space>
            <Button size="small" onClick={() => open(a)}>
              编辑
            </Button>
            {canMail && (
              <Popconfirm title={`把《${a.title}》群发给用户？`} description="发给有已验证邮箱且未停用的用户，只能发一次" onConfirm={() => sendMail(a)}>
                <Button size="small">群发邮件</Button>
              </Popconfirm>
            )}
            <Popconfirm
              title={`删除公告《${a.title}》？`}
              onConfirm={() => act(() => api.DELETE('/api/admin/announcements/{id}', { params: { ...csrfHeader(), path: { id: a.id } } }), '已删除')}
            >
              <Button size="small" danger>
                删除
              </Button>
            </Popconfirm>
          </Space>
        )
      },
    })
  }

  return (
    <>
      <Space className="page-header">
        <Typography.Title level={3}>公告</Typography.Title>
        {canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open(null)}>
            新建公告
          </Button>
        )}
      </Space>
      <ErrorAlert error={announcements.error} />
      <Table<Announcement>
        rowKey="id"
        scroll={TABLE_SCROLL}
        loading={announcements.isLoading}
        columns={columns}
        dataSource={announcements.data?.items ?? []}
        pagination={{
          current: page,
          pageSize,
          total: announcements.data?.total ?? 0,
          onChange: (p, size) => {
            setPage(p)
            setPageSize(size)
          },
        }}
      />
      <AnnouncementModal
        announcement={editing}
        isOpen={isOpen}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          announcements.reload()
        }}
      />
    </>
  )
}

interface AnnouncementModalProps {
  /** null = 新建。 */
  announcement: Announcement | null
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

function AnnouncementModal({ announcement, isOpen, onClose, onSaved }: AnnouncementModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title={announcement === null ? '新建公告' : '编辑公告'} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={MODAL_WIDTH}>
      <AnnouncementForm key={generation} announcement={announcement} onSaved={onSaved} />
    </Modal>
  )
}

function AnnouncementForm({ announcement, onSaved }: Pick<AnnouncementModalProps, 'announcement' | 'onSaved'>) {
  const { error, isBusy, run } = useSubmit(announcement === null ? '已创建公告' : '已保存', onSaved)
  const submit = (v: AnnouncementInput) => {
    const body: AnnouncementInput = { title: v.title, content: v.content ?? '', isPinned: v.isPinned, isPopup: v.isPopup, isPublished: v.isPublished }
    return run(() =>
      announcement === null
        ? api.POST('/api/admin/announcements', { params: csrfHeader(), body })
        : api.PUT('/api/admin/announcements/{id}', { params: { ...csrfHeader(), path: { id: announcement.id } }, body }),
    )
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<AnnouncementInput> layout="vertical" initialValues={formOf(announcement)} onFinish={(v) => void submit(v)}>
        <Form.Item name="title" label="标题" rules={[{ required: true, max: MAX_TITLE_LEN, message: `1–${MAX_TITLE_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="content" label="正文" extra={MARKDOWN_HINT} rules={[{ max: MAX_MARKDOWN_LEN }]}>
          <Input.TextArea rows={CONTENT_ROWS} className="mono" showCount maxLength={MAX_MARKDOWN_LEN} />
        </Form.Item>
        <Form.Item name="isPublished" label="发布" extra="关闭即存为草稿，用户看不到" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="isPinned" label="置顶" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="isPopup" label="进入仪表盘时弹出" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {announcement === null ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}
