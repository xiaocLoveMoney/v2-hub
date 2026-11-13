import type { ReactNode } from 'react'
import { api, type SiteInfo } from '../api/client'
import { useRemote } from '../hooks/use-remote'
import { DEFAULT_SITE, SiteContext } from './site-context'

const loadSite = (signal: AbortSignal) => api.GET('/api/user/site', { signal })

/** 启动时取一次站点信息，供登录 / 注册页与菜单判断哪些功能开着。 */
export function SiteProvider({ children }: { children: ReactNode }) {
  const site = useRemote<SiteInfo>(loadSite)
  return <SiteContext.Provider value={site.data ?? DEFAULT_SITE}>{children}</SiteContext.Provider>
}
