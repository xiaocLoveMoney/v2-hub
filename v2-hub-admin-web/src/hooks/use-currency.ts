import { api, type Settings } from '../api/client'
import { useSession } from '../auth/session-context'
import { PERMISSIONS } from '../menu'
import { DEFAULT_CURRENCY } from '../money'
import { useRemote } from './use-remote'

const loadSettings = (signal: AbortSignal) => api.GET('/api/admin/settings', { signal })

/** 金额的显示币种（设置 shop.currency）；读不到设置时用默认币种。只影响显示，不做换算。 */
export function useCurrency(): string {
  const { can } = useSession()
  // 没有 setting:read 时不请求设置，金额按默认币种显示
  const settings = useRemote<Settings>(loadSettings, can(PERMISSIONS.SETTING_READ))
  return settings.data?.shop.currency ?? DEFAULT_CURRENCY
}
