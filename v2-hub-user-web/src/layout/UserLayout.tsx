import { LogOut, Menu as MenuIcon } from 'lucide-react'
import { Fragment, useState } from 'react'
import { NavLink, Navigate, Outlet, useLocation, useNavigate } from 'react-router'
import { useSession } from '../auth/session-context'
import { LocaleSwitch } from '../components/LocaleSwitch'
import { revealChildren } from '../components/motion'
import { SupportButton } from '../components/SupportButton'
import { Alert, Button, Loading } from '../components/ui'
import { useI18n, type MessageKey } from '../i18n/i18n-context'
import { LandingPage } from '../pages/LandingPage'
import { ROUTES, visibleMenu, type MenuEntry } from '../menu'
import { useSite } from '../site/site-context'

const STATUS_DISABLED = 'disabled'

/** 把菜单表按分组切开，保持表内顺序。 */
function groupMenu(entries: readonly MenuEntry[]): { title: MessageKey | undefined; items: MenuEntry[] }[] {
  const groups: { title: MessageKey | undefined; items: MenuEntry[] }[] = []
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
 * 已登录后的整体布局：侧栏 + 主色色带 + 顶栏 + 内容区（docs/06 §4 外壳）。未登录跳到登录页，
 * 但站点开了落地页时根地址改为显示落地页（docs/03 §5.20）；停用的账号只显示停用提示（docs/03 §5.3）。
 */
export function UserLayout() {
  const { status, session, logout } = useSession()
  const site = useSite()
  const location = useLocation()
  const navigate = useNavigate()
  const { t, rich } = useI18n()
  // 小屏：侧栏默认收起、点按钮滑出；大屏：默认展开、点按钮收起。两种状态分开记
  const [isOpenOnSmall, setIsOpenOnSmall] = useState(false)
  const [isCollapsedOnLarge, setIsCollapsedOnLarge] = useState(false)

  if (status === 'loading') {
    return <Loading />
  }
  const isAnonymous = status === 'anonymous' || session === null
  if (isAnonymous && location.pathname === ROUTES.DASHBOARD && site.landing?.enabled === true) {
    return <LandingPage />
  }
  if (isAnonymous) {
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
        <nav aria-label={t('shell.mainMenu')}>
          <ul className="hub-menu">
            {groupMenu(visibleMenu(site, session.features)).map((g) => (
              <Fragment key={g.title ?? 'top'}>
                {g.title !== undefined && <li className="hub-menu-header">{t(g.title)}</li>}
                {g.items.map((entry) => (
                  <li key={entry.path}>
                    <NavLink to={entry.path} end={entry.path === ROUTES.DASHBOARD} className="hub-menu-link" onClick={() => setIsOpenOnSmall(false)}>
                      {entry.icon}
                      <span>{t(entry.labelKey)}</span>
                    </NavLink>
                  </li>
                ))}
              </Fragment>
            ))}
          </ul>
        </nav>
      </aside>
      <button type="button" className="hub-sider-mask" aria-label={t('shell.closeMenu')} onClick={() => setIsOpenOnSmall(false)} />
      <div className="hub-main">
        <div className="hub-band" aria-hidden="true" />
        <header className="hub-topbar">
          <button type="button" className="hub-topbar-toggle" aria-label={t('shell.toggleMenu')} onClick={toggleSider}>
            <MenuIcon size={20} />
          </button>
          <div className="hub-topbar-user">
            <span>{t('shell.greeting', { name: session.user.username })}</span>
            <Button variant="ghost" onClick={() => void handleLogout()}>
              <LogOut size={14} />
              {t('shell.logout')}
            </Button>
          </div>
        </header>
        {session.impersonated === true && (
          <div className="hub-impersonate-bar" role="status">
            <span>{rich('shell.impersonating', { name: <strong>{session.user.username}</strong> })}</span>
            <Button variant="ghost" onClick={() => void handleLogout()}>
              {t('shell.logout')}
            </Button>
          </div>
        )}
        <main className="hub-content" ref={revealChildren}>
          {isDisabled ? <Alert tone="danger">{t('shell.accountDisabled')}</Alert> : <Outlet />}
        </main>
        <footer className="hub-footer">
          <span>{t('shell.appTitle')}</span>
          <LocaleSwitch />
        </footer>
      </div>
      <SupportButton />
    </div>
  )
}
