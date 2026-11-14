import { ArrowDownOutlined, ArrowUpOutlined } from '@ant-design/icons'
import { Button, Card, Space, Tag, Typography } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, type Settings } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { CardPage } from '../../components/CardPage'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useRemote } from '../../hooks/use-remote'
import { useSubmit } from '../../hooks/use-submit'
import { MENU, PERMISSIONS } from '../../menu'
import { applyMenuOrder } from '../../menu-order'
import { USER_MENU_CATALOG, type CatalogEntry } from './user-menu-catalog'

/** 顶层菜单项（不属于任何分组）在页面上显示的组名。 */
const TOP_GROUP_TITLE = '顶层'
const UP = -1
const DOWN = 1

/** 管理端的菜单目录取自菜单表本身（docs/03 §5.6 菜单键目录）。 */
const ADMIN_MENU_CATALOG: readonly CatalogEntry[] = MENU.map((e) => ({ key: e.key, label: e.label, group: e.group }))

interface MenuGroup {
  /** 分组名；顶层为 undefined。 */
  title: string | undefined
  entries: CatalogEntry[]
}

/** 把排好的菜单切成连续的分组。 */
function toGroups(entries: readonly CatalogEntry[]): MenuGroup[] {
  const groups: MenuGroup[] = []
  for (const entry of entries) {
    const last = groups[groups.length - 1]
    if (last !== undefined && last.title === entry.group) {
      last.entries.push(entry)
      continue
    }
    groups.push({ title: entry.group, entries: [entry] })
  }
  return groups
}

const flatten = (groups: readonly MenuGroup[]) => groups.flatMap((g) => g.entries.map((e) => e.key))

/** 返回把 index 处元素挪一位后的新数组；越界时原样返回。 */
function moved<T>(list: readonly T[], index: number, step: number): T[] {
  const target = index + step
  if (target < 0 || target >= list.length) {
    return [...list]
  }
  const next = [...list]
  ;[next[index], next[target]] = [next[target], next[index]]
  return next
}

/** 与默认顺序相同时存空列表，以后新加的菜单仍落在默认位置（Ruling-57）。 */
function toSaved(groups: readonly MenuGroup[], catalog: readonly CatalogEntry[]): string[] {
  const keys = flatten(groups)
  const isDefault = keys.join(',') === catalog.map((e) => e.key).join(',')
  return isDefault ? [] : keys
}

const loadSettings = (signal: AbortSignal) => api.GET('/api/admin/settings', { signal })

/** 菜单顺序（docs/03 §5.6 菜单顺序，Ruling-57）：调整管理端与客户门户侧栏的分组先后与组内先后，全站统一。 */
export function MenuOrderPage() {
  const settings = useRemote<Settings>(loadSettings)
  return settings.data === null ? (
    <CardPage title="菜单顺序">
      <ErrorAlert error={settings.error} />
    </CardPage>
  ) : (
    <MenuOrderEditor menu={settings.data.menu} onSaved={settings.reload} />
  )
}

interface MenuOrderEditorProps {
  menu: Settings['menu']
  onSaved: () => void
}

function MenuOrderEditor({ menu, onSaved }: MenuOrderEditorProps) {
  const { can, refresh } = useSession()
  const canWrite = can(PERMISSIONS.SETTING_WRITE)
  const [adminGroups, setAdminGroups] = useState(() => toGroups(applyMenuOrder(ADMIN_MENU_CATALOG, menu.admin)))
  const [userGroups, setUserGroups] = useState(() => toGroups(applyMenuOrder(USER_MENU_CATALOG, menu.user)))
  // 保存后刷新会话，侧栏立即按新顺序排
  const { error, isBusy, run } = useSubmit('已保存', () => {
    void refresh()
    onSaved()
  })

  const save = () =>
    run(() =>
      api.PUT('/api/admin/settings', {
        params: csrfHeader(),
        body: { menu: { admin: toSaved(adminGroups, ADMIN_MENU_CATALOG), user: toSaved(userGroups, USER_MENU_CATALOG) } },
      }),
    )
  const resetAll = () => {
    setAdminGroups(toGroups(ADMIN_MENU_CATALOG))
    setUserGroups(toGroups(USER_MENU_CATALOG))
  }

  const actions = canWrite && (
    <Space>
      <Button onClick={resetAll}>恢复默认</Button>
      <Button type="primary" loading={isBusy} onClick={() => void save()}>
        保存
      </Button>
    </Space>
  )
  return (
    <CardPage title="菜单顺序" extra={actions}>
      <ErrorAlert error={error} />
      <div className="card-grid">
        <MenuOrderCard title="管理端菜单" hint="对所有管理员生效；各人仍只看到有权限的菜单" groups={adminGroups} onChange={setAdminGroups} canWrite={canWrite} />
        <MenuOrderCard title="客户门户菜单" hint="对所有用户生效；带条件的菜单只在条件满足时出现" groups={userGroups} onChange={setUserGroups} canWrite={canWrite} />
      </div>
    </CardPage>
  )
}

interface MenuOrderCardProps {
  title: string
  hint: string
  groups: MenuGroup[]
  onChange: (groups: MenuGroup[]) => void
  canWrite: boolean
}

/** 一端的菜单：分组与组内菜单项各有上移 / 下移。 */
function MenuOrderCard({ title, hint, groups, onChange, canWrite }: MenuOrderCardProps) {
  const moveGroup = (index: number, step: number) => onChange(moved(groups, index, step))
  const moveEntry = (groupIndex: number, index: number, step: number) =>
    onChange(groups.map((g, i) => (i === groupIndex ? { ...g, entries: moved(g.entries, index, step) } : g)))

  return (
    <Card title={title}>
      <Typography.Paragraph type="secondary">{hint}</Typography.Paragraph>
      {groups.map((group, gi) => {
        const groupName = group.title ?? TOP_GROUP_TITLE
        return (
          <section key={group.title ?? TOP_GROUP_TITLE} className="menu-order-group" aria-label={groupName}>
            <div className="menu-order-group-header">
              <Typography.Text strong>{groupName}</Typography.Text>
              <MoveButtons
                name={`分组 ${groupName}`}
                canUp={canWrite && gi > 0}
                canDown={canWrite && gi < groups.length - 1}
                onMove={(step) => moveGroup(gi, step)}
              />
            </div>
            <ul className="menu-order-list">
              {group.entries.map((entry, ei) => (
                <li key={entry.key} className="menu-order-item">
                  <span>
                    {entry.label}
                    {entry.condition !== undefined && <Tag className="menu-order-tag">{entry.condition}</Tag>}
                  </span>
                  <MoveButtons
                    name={entry.label}
                    canUp={canWrite && ei > 0}
                    canDown={canWrite && ei < group.entries.length - 1}
                    onMove={(step) => moveEntry(gi, ei, step)}
                  />
                </li>
              ))}
            </ul>
          </section>
        )
      })}
    </Card>
  )
}

interface MoveButtonsProps {
  /** 用于按钮的无障碍名称，如“上移 公告”。 */
  name: string
  canUp: boolean
  canDown: boolean
  onMove: (step: number) => void
}

function MoveButtons({ name, canUp, canDown, onMove }: MoveButtonsProps) {
  return (
    <Space size={0}>
      <Button type="text" size="small" icon={<ArrowUpOutlined />} aria-label={`上移 ${name}`} disabled={!canUp} onClick={() => onMove(UP)} />
      <Button type="text" size="small" icon={<ArrowDownOutlined />} aria-label={`下移 ${name}`} disabled={!canDown} onClick={() => onMove(DOWN)} />
    </Space>
  )
}
