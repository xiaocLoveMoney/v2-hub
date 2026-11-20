import { api, type TutorialPlatform } from '../api/client'
import type { MessageKey } from '../i18n/i18n-context'
import { ROUTES } from '../menu'

/** 平台的显示名与显示顺序；取值见 hub-api.yaml 的 TutorialPlatform。 */
export const PLATFORMS: { value: TutorialPlatform; labelKey: MessageKey }[] = [
  { value: 'windows', labelKey: 'tutorials.windows' },
  { value: 'macos', labelKey: 'tutorials.macos' },
  { value: 'android', labelKey: 'tutorials.android' },
  { value: 'ios', labelKey: 'tutorials.ios' },
  { value: 'linux', labelKey: 'tutorials.linux' },
  { value: 'router', labelKey: 'tutorials.router' },
]

/** 列表页地址里记录选中平台的查询参数（Ruling-90）：从详情页返回时保持原来的平台。 */
export const PLATFORM_PARAM = 'os'

/** 列表与详情页共用的接口（Ruling-90：详情页不另加接口）。 */
export const loadTutorials = (signal: AbortSignal) => api.GET('/api/user/tutorials', { signal })

/** 把地址里的平台参数解析为已知平台；不认识的值当作没选。 */
export function parsePlatform(value: string | null): TutorialPlatform | null {
  return PLATFORMS.find((p) => p.value === value)?.value ?? null
}

/** 某平台的教程列表地址。 */
export function tutorialListPath(platform: TutorialPlatform): string {
  return `${ROUTES.TUTORIALS}?${PLATFORM_PARAM}=${platform}`
}

/** 某篇教程的详情页地址。 */
export function tutorialDetailPath(id: number): string {
  return ROUTES.TUTORIAL_DETAIL.replace(':id', String(id))
}
