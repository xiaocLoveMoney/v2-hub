import { useSearchParams } from 'react-router'

const TAB_PARAM = 'tab'

/**
 * 把当前页签记在地址栏的 ?tab= 里（Ruling-88）：刷新与分享链接都能回到同一个页签。
 * 默认页签不写进地址栏；切换页签用 replace，不往浏览历史里加记录。
 * @param keys 当前能打开的页签（没有权限的不在其中）
 * @param defaultKey 地址栏没写或写了不认识的值时用的页签
 * @returns 当前页签，与切换页签的函数（可直接交给 Tabs 的 onChange）
 */
export function useTabParam(keys: readonly string[], defaultKey: string): [string, (key: string) => void] {
  const [searchParams, setSearchParams] = useSearchParams()
  const requested = searchParams.get(TAB_PARAM) ?? defaultKey
  const activeTab = keys.includes(requested) ? requested : defaultKey
  const setTab = (key: string) => setSearchParams(key === defaultKey ? {} : { [TAB_PARAM]: key }, { replace: true })
  return [activeTab, setTab]
}
