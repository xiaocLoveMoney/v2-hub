import { Select, Spin } from 'antd'
import { useEffect, useState } from 'react'
import { api } from '../../api/client'

/** 输入停顿多久后才去搜（毫秒），避免每敲一个字发一次请求。 */
const SEARCH_DELAY_MS = 300
const SEARCH_PAGE_SIZE = 20
const MAX_KEYWORD_LEN = 64

/** 一个可选的用户：id 与用户名。 */
export interface UserOption {
  id: number
  username: string
}

interface ExitUserSelectProps {
  /** 由 Form.Item 注入。 */
  value?: number[]
  onChange?: (ids: number[]) => void
  /** 已选用户的名字（编辑时来自出口详情），用于在搜索结果之外也能显示标签。 */
  initialUsers: readonly UserOption[]
}

/**
 * 平台出口“指定用户”的多选框（Ruling-64）：按用户名远程搜索（GET /api/admin/users，需要 user:read），
 * 已选的用户始终保留标签。
 */
export function ExitUserSelect({ value, onChange, initialUsers }: ExitUserSelectProps) {
  const [keyword, setKeyword] = useState('')
  const [found, setFound] = useState<UserOption[]>([])
  const [known, setKnown] = useState<ReadonlyMap<number, string>>(() => new Map(initialUsers.map((u) => [u.id, u.username])))
  const [isSearching, setIsSearching] = useState(false)
  const [searchError, setSearchError] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    const timer = setTimeout(() => {
      setIsSearching(true)
      api
        .GET('/api/admin/users', {
          params: { query: { page: 1, pageSize: SEARCH_PAGE_SIZE, keyword: keyword === '' ? undefined : keyword } },
          signal: controller.signal,
        })
        .then(({ data, error }) => {
          const items = (data?.data?.items ?? []).map((u) => ({ id: u.id, username: u.username }))
          setFound(items)
          setSearchError(error !== undefined)
          setKnown((prev) => new Map([...prev, ...items.map((u): [number, string] => [u.id, u.username])]))
        })
        .catch(() => {
          // 网络失败或被取消：取消时什么都不做，否则在下拉里提示搜索失败
          if (!controller.signal.aborted) {
            setSearchError(true)
          }
        })
        .finally(() => {
          if (!controller.signal.aborted) {
            setIsSearching(false)
          }
        })
    }, SEARCH_DELAY_MS)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [keyword])

  const selected = value ?? []
  const foundIds = new Set(found.map((u) => u.id))
  const options = [
    ...found.map((u) => ({ value: u.id, label: u.username })),
    ...selected.filter((id) => !foundIds.has(id)).map((id) => ({ value: id, label: known.get(id) ?? `#${id}` })),
  ]
  return (
    <Select<number[]>
      mode="multiple"
      aria-label="指定用户"
      placeholder="输入用户名搜索"
      showSearch={{ filterOption: false, onSearch: (v) => setKeyword(v.trim().slice(0, MAX_KEYWORD_LEN)) }}
      notFoundContent={isSearching ? <Spin size="small" /> : searchError ? '搜索失败（需要“用户”的查看权限）' : '没有匹配的用户'}
      options={options}
      value={selected}
      onChange={(ids) => onChange?.(ids)}
    />
  )
}
