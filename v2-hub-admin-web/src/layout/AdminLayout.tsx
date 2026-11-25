import { LogoutOutlined } from '@ant-design/icons'
import { Alert, Button, Layout, Menu, Space, Spin, Typography, type MenuProps } from 'antd'
import { useMemo } from 'react'
import { Navigate, Outlet, useLocation, useNavigate } from 'react-router'
import { useSession } from '../auth/session-context'
import { BrandMark } from '../components/BrandMark'
import { revealChildren } from '../components/motion'
import { MENU, ROUTES, visibleMenu, type MenuEntry } from '../menu'

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

/** 已登录后的整体布局：左侧按权限渲染的菜单 + 顶栏 + 内容区。未登录跳到登录页；超管未开启两步验证时只能停在账号安全页。 */
export function AdminLayout() {
  const { status, session, can, logout } = useSession()
  const location = useLocation()
  const navigate = useNavigate()
  const menuOrder = session?.menuOrder
  const isTotpSetupRequired = session?.totpSetupRequired ?? false
  const items = useMemo(
    () => toMenuItems(isTotpSetupRequired ? TOTP_SETUP_MENU : visibleMenu(can, menuOrder)),
    [can, menuOrder, isTotpSetupRequired],
  )

  if (status === 'loading') {
    return <Spin fullscreen />
  }
  if (status === 'anonymous') {
    return <Navigate to={ROUTES.LOGIN} replace state={{ from: location.pathname }} />
  }
  // 超管须先开启两步验证（Ruling-98）：其它页面一律停到账号安全页，开启后刷新会话即恢复
  if (isTotpSetupRequired && location.pathname !== ROUTES.SECURITY) {
    return <Navigate to={ROUTES.SECURITY} replace />
  }

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
            <Typography.Text className="admin-header-user">{session?.admin.username}</Typography.Text>
            <Button ghost icon={<LogoutOutlined />} onClick={() => void handleLogout()}>
              退出
            </Button>
          </Space>
        </Layout.Header>
        {/* 页面根节点插入时进场（components/motion.ts）；不另包一层，样式里的 .admin-content:has(> .card-page) 要求页面是直接子元素 */}
        <Layout.Content className="admin-content" ref={revealChildren}>
          {isTotpSetupRequired && (
            <Alert
              type="warning"
              showIcon
              className="page-alert"
              title="超级管理员必须先开启两步验证"
              description="开启之前只能使用账号安全页；在下方“两步验证”里完成开启后即可正常使用管理端。"
            />
          )}
          <Outlet />
        </Layout.Content>
      </Layout>
    </Layout>
  )
}
