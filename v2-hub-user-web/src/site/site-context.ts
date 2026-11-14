import { createContext, useContext } from 'react'
import type { SiteInfo } from '../api/client'
import { BRAND } from '../app-config'
import { DEFAULT_CURRENCY } from '../money'

/** 站点信息取不到时的兜底：按全部关闭处理，不显示用不了的入口（docs/06 §5 M4 的做法）。 */
export const DEFAULT_SITE: SiteInfo = {
  siteName: BRAND,
  registerMode: 'closed',
  emailVerifyRequired: false,
  mailEnabled: false,
  checkinEnabled: false,
  ticketEnabled: false,
  sharedEnabled: false,
  shopEnabled: false,
  currency: DEFAULT_CURRENCY,
  menuOrder: [],
}

export const SiteContext = createContext<SiteInfo>(DEFAULT_SITE)

/** 读取站点信息（/api/user/site 的公开子集）；还没取到或取不到时是 DEFAULT_SITE。 */
export function useSite(): SiteInfo {
  return useContext(SiteContext)
}

/** 金额的显示币种（站点信息里的 currency）；只影响显示，不做换算。 */
export function useCurrency(): string {
  return useSite().currency ?? DEFAULT_CURRENCY
}
