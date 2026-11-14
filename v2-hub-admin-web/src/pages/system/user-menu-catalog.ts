import type { OrderableEntry } from '../../menu-order'

/** 菜单顺序页里显示的一个菜单项。 */
export interface CatalogEntry extends OrderableEntry {
  label: string
  /** 只在满足条件时才出现在侧栏，菜单顺序页里给个提示。 */
  condition?: string
}

const GROUP_MINE = '我的'
const GROUP_USE = '使用'

/**
 * 客户门户的菜单目录（默认顺序），与 docs/03 §5.6 菜单键目录及 v2-hub-user-web 的菜单表一致（Ruling-57）；
 * 客户门户增减菜单时同步改这里。
 */
export const USER_MENU_CATALOG: readonly CatalogEntry[] = [
  { key: 'dashboard', label: '仪表盘' },
  { key: 'shop', label: '商店', condition: '开启商店时显示' },
  { key: 'announcements', label: '公告' },
  { key: 'account', label: '我的账号', group: GROUP_MINE },
  { key: 'wallet', label: '我的钱包', group: GROUP_MINE, condition: '开启商店时显示' },
  { key: 'invite', label: '邀请注册', group: GROUP_MINE },
  { key: 'nodes', label: '节点列表', group: GROUP_USE },
  { key: 'tutorials', label: '下载和使用', group: GROUP_USE },
  { key: 'subscribe-logs', label: '订阅记录', group: GROUP_USE },
  { key: 'shared-accounts', label: '共享账号', group: GROUP_USE, condition: '开启共享账号页时显示' },
  { key: 'tickets', label: '工单支持', group: GROUP_USE, condition: '开启工单时显示' },
  { key: 'chain', label: '链式代理', group: GROUP_USE, condition: '套餐开了链式时显示' },
]
