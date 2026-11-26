import { LogoutOutlined } from '@ant-design/icons'
import { Alert, Button, Layout, Menu, Space, Tag, Typography, type MenuProps } from 'antd'
import { useMemo, type ReactNode } from 'react'
import { Navigate, Outlet, useLocation, useNavigate } from 'react-router'
import { useSession } from '../auth/session-context'
import { BrandMark } from '../components/BrandMark'
import { revealChildren } from '../components/motion'
import { AGENT_MENU, AGENT_ROUTES } from '../agent-menu'
import { MENU, ROUTES, visibleMenu, type MenuEntry } from '../menu'
import { ForbiddenPage } from '../pages/Placeholders'
import { SessionGuard } from './SessionGuard'

type MenuItem = Required<MenuProps>['items'][number]

/** 超管尚未开启两步验证时侧栏只留账号安全（Ruling-98）。 */
const TOTP_SETUP_MENU: MenuEntry[] = MENU.filter((entry) => entry.path === ROUTES.SECURITY)

/** 侧栏宽度（docs/06 §4 外壳）。 */
const SIDER_WIDTH = 250
/** 侧栏品牌位的图标尺寸（像素）。 */
const BRAND_MARK_SIZE = 26

/** 把菜单表转成 antd Menu 的 items：有 group 的归到同名分组下，保持表内顺序。 */
function toMenuItems(entries: MenuEntry[]): MenuItem[] {
  const items: MenuItem[] = []
  const groups = new Map<string, MenuItem[]>()
  for (const entry of entries) {
    const item: MenuItem = { key: entry.path, icon: entry.icon, label: entry.label }
    if (entry.group === undefined) {
      items.push(item)
      continue
    }
    let children = groups.get(entry.group)
    if (children === undefined) {
      children = []
      groups.set(entry.group, children)
      items.push({ type: 'group', key: `group:${entry.group}`, label: entry.group, children })
    }
    children.push(item)
  }
  return items
}

/** 代理访问员工路径时 403 页的说明文字（Ruling-101）。 */
const AGENT_FORBIDDEN_TEXT = '代理账号只能使用代理界面'

/**
 * 员工的布局：会话判断见 SessionGuard；代理账号不渲染任何员工页面（根路径转到代理首页，其余员工路径显示 403，Ruling-101）。
 */
export function AdminLayout() {
  const location = useLocation()
  const wrongKind =
    location.pathname === ROUTES.DASHBOARD ? (
      <Navigate to={AGENT_ROUTES.HOME} replace />
    ) : (
      <AgentShell>
        <ForbiddenPage subTitle={AGENT_FORBIDDEN_TEXT} homePath={AGENT_ROUTES.HOME} homeLabel="返回配额概览" />
      </AgentShell>
    )
  return (
    <SessionGuard kind="staff" wrongKind={wrongKind}>
      <AdminShell>
        <Outlet />
      </AdminShell>
    </SessionGuard>
  )
}

interface ShellProps {
  children: ReactNode
}

/** 员工的外壳：左侧按权限与菜单顺序渲染的菜单 + 顶栏 + 内容区。调用方已确认是已登录的员工。 */
export function AdminShell({ children }: ShellProps) {
  const { session, can } = useSession()
  const menuOrder = session?.menuOrder
  const isTotpSetupRequired = session?.totpSetupRequired ?? false
  const items = useMemo(
    () => toMenuItems(isTotpSetupRequired ? TOTP_SETUP_MENU : visibleMenu(can, menuOrder)),
    [can, menuOrder, isTotpSetupRequired],
  )
  return (
    <ShellFrame items={items}>
      {isTotpSetupRequired && (
        <Alert
          type="warning"
          showIcon
          className="page-alert"
          title="超级管理员必须先开启两步验证"
          description="开启之前只能使用账号安全页；在下方“两步验证”里完成开启后即可正常使用管理端。"
        />
      )}
      {children}
    </ShellFrame>
  )
}

/** 代理侧栏的菜单项：固定列表，不分组、不排序（docs/03 §5.6）。 */
const AGENT_MENU_ITEMS: MenuItem[] = AGENT_MENU.map((entry) => ({ key: entry.path, icon: entry.icon, label: entry.label }))

/** 代理的外壳：固定菜单 + 顶栏 + 内容区；只读会话，不发任何员工接口的请求（Ruling-101）。 */
export function AgentShell({ children }: ShellProps) {
  return <ShellFrame items={AGENT_MENU_ITEMS} roleLabel="代理">{children}</ShellFrame>
}

interface ShellFrameProps extends ShellProps {
  items: MenuItem[]
  /** 顶栏用户名旁的身份标记；员工不显示。 */
  roleLabel?: string
}

/** 两种外壳共用的框架：侧栏、色带、顶栏（用户名与退出）、内容区。 */
function ShellFrame({ items, roleLabel, children }: ShellFrameProps) {
  const { session, logout } = useSession()
  const location = useLocation()
  const navigate = useNavigate()

  const handleLogout = async () => {
    await logout()
    void navigate(ROUTES.LOGIN, { replace: true })
  }

  return (
    <Layout className="admin-layout">
      <Layout.Sider breakpoint="lg" collapsedWidth={0} theme="light" width={SIDER_WIDTH} className="admin-sider">
        <div className="admin-brand">
          <BrandMark size={BRAND_MARK_SIZE} motion="intro" hasHoverTurn />
          <span>v2-hub</span>
        </div>
        <Menu
          mode="inline"
          selectedKeys={[location.pathname]}
          items={items}
          onClick={({ key }) => void navigate(key)}
        />
      </Layout.Sider>
      <Layout className="admin-main">
        <div className="admin-band" aria-hidden="true" />
        <Layout.Header className="admin-header">
          <Space>
            {roleLabel !== undefined && <Tag>{roleLabel}</Tag>}
            <Typography.Text className="admin-header-user">{session?.admin.username}</Typography.Text>
            <Button ghost icon={<LogoutOutlined />} onClick={() => void handleLogout()}>
              退出
            </Button>
          </Space>
        </Layout.Header>
        {/* 页面根节点插入时进场（components/motion.ts）；不另包一层，样式里的 .admin-content:has(> .card-page) 要求页面是直接子元素 */}
        <Layout.Content className="admin-content" ref={revealChildren}>
          {children}
        </Layout.Content>
      </Layout>
    </Layout>
  )
}
