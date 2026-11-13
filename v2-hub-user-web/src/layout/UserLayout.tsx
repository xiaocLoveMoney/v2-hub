import { LogOut, Menu as MenuIcon } from 'lucide-react'
import { Fragment, useState } from 'react'
import { NavLink, Navigate, Outlet, useLocation, useNavigate } from 'react-router'
import { APP_TITLE } from '../app-config'
import { useSession } from '../auth/session-context'
import { Alert, Button } from '../components/ui'
import { ROUTES, visibleMenu, type MenuEntry } from '../menu'
import { useSite } from '../site/site-context'

const STATUS_DISABLED = 'disabled'

/** 把菜单表按分组切开，保持表内顺序。 */
function groupMenu(entries: readonly MenuEntry[]): { title: string | undefined; items: MenuEntry[] }[] {
  const groups: { title: string | undefined; items: MenuEntry[] }[] = []
  for (const entry of entries) {
    const last = groups[groups.length - 1]
    if (last !== undefined && last.title === entry.group) {
      last.items.push(entry)
      continue
    }
    groups.push({ title: entry.group, items: [entry] })
  }
  return groups
}

/**
 * 已登录后的整体布局：侧栏 + 主色色带 + 顶栏 + 内容区（docs/06 §4 外壳）。未登录跳到登录页；
 * 停用的账号只显示停用提示（docs/03 §5.3）。
 */
export function UserLayout() {
  const { status, session, logout } = useSession()
  const site = useSite()
  const location = useLocation()
  const navigate = useNavigate()
  // 小屏：侧栏默认收起、点按钮滑出；大屏：默认展开、点按钮收起。两种状态分开记
  const [isOpenOnSmall, setIsOpenOnSmall] = useState(false)
  const [isCollapsedOnLarge, setIsCollapsedOnLarge] = useState(false)

  if (status === 'loading') {
    return <div className="hub-empty">加载中…</div>
  }
  if (status === 'anonymous' || session === null) {
    return <Navigate to={ROUTES.LOGIN} replace state={{ from: location.pathname }} />
  }

  const handleLogout = async () => {
    await logout()
    void navigate(ROUTES.LOGIN, { replace: true })
  }
  const toggleSider = () => {
    setIsOpenOnSmall((v) => !v)
    setIsCollapsedOnLarge((v) => !v)
  }
  const isDisabled = session.user.status === STATUS_DISABLED
  const shellClass = ['hub-shell', isOpenOnSmall ? 'sider-open' : '', isCollapsedOnLarge ? 'sider-collapsed' : ''].filter((c) => c !== '').join(' ')

  return (
    <div className={shellClass}>
      <aside className="hub-sider">
        <div className="hub-brand">{site.siteName}</div>
        <nav aria-label="主菜单">
          <ul className="hub-menu">
            {groupMenu(visibleMenu(site, session.features)).map((g) => (
              <Fragment key={g.title ?? 'top'}>
                {g.title !== undefined && <li className="hub-menu-header">{g.title}</li>}
                {g.items.map((entry) => (
                  <li key={entry.path}>
                    <NavLink to={entry.path} end={entry.path === ROUTES.DASHBOARD} className="hub-menu-link" onClick={() => setIsOpenOnSmall(false)}>
                      {entry.icon}
                      <span>{entry.label}</span>
                    </NavLink>
                  </li>
                ))}
              </Fragment>
            ))}
          </ul>
        </nav>
      </aside>
      <button type="button" className="hub-sider-mask" aria-label="收起菜单" onClick={() => setIsOpenOnSmall(false)} />
      <div className="hub-main">
        <div className="hub-band" aria-hidden="true" />
        <header className="hub-topbar">
          <button type="button" className="hub-topbar-toggle" aria-label="展开或收起菜单" onClick={toggleSider}>
            <MenuIcon size={20} />
          </button>
          <div className="hub-topbar-user">
            <span>Hi, {session.user.username}</span>
            <Button variant="ghost" onClick={() => void handleLogout()}>
              <LogOut size={14} />
              退出
            </Button>
          </div>
        </header>
        {session.impersonated === true && (
          <div className="hub-impersonate-bar" role="status">
            <span>
              正在以 <strong>{session.user.username}</strong> 的身份查看（只读，30 分钟后失效）
            </span>
            <Button variant="ghost" onClick={() => void handleLogout()}>
              退出
            </Button>
          </div>
        )}
        <main className="hub-content">
          {isDisabled ? <Alert tone="danger">账号已停用，请联系管理员。</Alert> : <Outlet />}
        </main>
        <footer className="hub-footer">{APP_TITLE}</footer>
      </div>
    </div>
  )
}
