import { LogoutOutlined } from '@ant-design/icons'
import { Button, Layout, Menu, Space, Spin, Typography, type MenuProps } from 'antd'
import { useMemo } from 'react'
import { Navigate, Outlet, useLocation, useNavigate } from 'react-router'
import { useSession } from '../auth/session-context'
import { ROUTES, visibleMenu, type MenuEntry } from '../menu'

type MenuItem = Required<MenuProps>['items'][number]

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

/** 已登录后的整体布局：左侧按权限渲染的菜单 + 顶栏 + 内容区。未登录跳到登录页。 */
export function AdminLayout() {
  const { status, session, can, logout } = useSession()
  const location = useLocation()
  const navigate = useNavigate()
  const items = useMemo(() => toMenuItems(visibleMenu(can)), [can])

  if (status === 'loading') {
    return <Spin fullscreen />
  }
  if (status === 'anonymous') {
    return <Navigate to={ROUTES.LOGIN} replace state={{ from: location.pathname }} />
  }

  const handleLogout = async () => {
    await logout()
    void navigate(ROUTES.LOGIN, { replace: true })
  }

  return (
    <Layout className="admin-layout">
      <Layout.Sider breakpoint="lg" collapsedWidth={0} theme="light" width={220}>
        <div className="admin-brand">v2-hub</div>
        <Menu
          mode="inline"
          selectedKeys={[location.pathname]}
          items={items}
          onClick={({ key }) => void navigate(key)}
        />
      </Layout.Sider>
      <Layout>
        <Layout.Header className="admin-header">
          <Space>
            <Typography.Text>{session?.admin.username}</Typography.Text>
            <Button icon={<LogoutOutlined />} onClick={() => void handleLogout()}>
              退出
            </Button>
          </Space>
        </Layout.Header>
        <Layout.Content className="admin-content">
          <Outlet />
        </Layout.Content>
      </Layout>
    </Layout>
  )
}
