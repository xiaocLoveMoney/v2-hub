import { useState } from 'react'
import { Link } from 'react-router'
import type { Announcement } from '../api/client'
import { Markdown } from '../components/Markdown'
import { Button, Card, Modal } from '../components/ui'
import { formatTime } from '../format'
import { ROUTES } from '../menu'

/** 记住“已关掉的弹窗公告 id”的 localStorage 键；只存公告 id，不属凭据（docs/06 §5 M4 的做法）。 */
const POPUP_DISMISSED_KEY = 'hub.popup.dismissed'

function readDismissedId(): string | null {
  try {
    return globalThis.localStorage.getItem(POPUP_DISMISSED_KEY)
  } catch (err) {
    // 浏览器禁用了本地存储：当作没关过，最多每次进仪表盘都弹一次
    reportError(err)
    return null
  }
}

function writeDismissedId(id: number) {
  try {
    globalThis.localStorage.setItem(POPUP_DISMISSED_KEY, String(id))
  } catch (err) {
    reportError(err)
  }
}

/** 仪表盘上的最新公告卡。 */
export function LatestAnnouncementCard({ announcement }: { announcement: Announcement }) {
  return (
    <Card title={`📢 ${announcement.title}`} extra={<Link to={ROUTES.ANNOUNCEMENTS}>全部公告 →</Link>}>
      <Markdown source={announcement.content} />
      <p className="hub-field-hint">{formatTime(announcement.publishedAt)}</p>
    </Card>
  )
}

/** 弹窗公告：同一条关掉后不再弹（docs/03 §5.9）。 */
export function PopupAnnouncement({ announcement }: { announcement: Announcement }) {
  const [isOpen, setIsOpen] = useState(() => readDismissedId() !== String(announcement.id))
  const close = () => {
    writeDismissedId(announcement.id)
    setIsOpen(false)
  }
  return (
    <Modal
      title={announcement.title}
      isOpen={isOpen}
      onClose={close}
      footer={
        <Button variant="primary" onClick={close}>
          知道了
        </Button>
      }
    >
      <Markdown source={announcement.content} />
    </Modal>
  )
}
