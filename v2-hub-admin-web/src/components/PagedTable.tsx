import { Space, Table, type TableProps } from 'antd'
import { useCallback, useState, type ReactNode } from 'react'
import type { PageData } from '../api/client'
import { useRemote } from '../hooks/use-remote'
import { TABLE_SCROLL } from '../pages/system/format'
import { ErrorAlert } from './ErrorAlert'

const DEFAULT_PAGE_SIZE = 20

/** openapi-fetch 分页接口的最小返回形状。 */
interface PageResult<T> {
  data?: { data?: PageData<T> }
  error?: unknown
}

interface PagedTableProps<T extends { id: number }> {
  /** 发起一页的请求；filterKey 变化时自动回到第一页，调用方用 useCallback 固定并列出筛选依赖。 */
  load: (page: number, pageSize: number, signal: AbortSignal) => Promise<PageResult<T>>
  columns: TableProps<T>['columns']
  /** 表格上方的筛选控件。 */
  filters?: ReactNode
  /** 外部改动数据后递增它来重新拉取（如新建、删除之后）。 */
  refreshKey?: number
}

/** 只读或带行内操作的服务端分页表格：统一分页状态、加载态与错误提示。筛选条件变了回到第一页。 */
export function PagedTable<T extends { id: number }>({ load, columns, filters, refreshKey = 0 }: PagedTableProps<T>) {
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE)
  // load 的引用变了说明筛选条件变了：在渲染期间把页码拨回 1（React 推荐的“按 props 调整状态”写法）
  const [lastLoad, setLastLoad] = useState(() => load)
  if (lastLoad !== load) {
    setLastLoad(() => load)
    setPage(1)
  }
  const request = useCallback(
    (signal: AbortSignal) => {
      // refreshKey 只用来让请求函数的引用随它变化，从而触发重新拉取
      void refreshKey
      return load(page, pageSize, signal)
    },
    [load, page, pageSize, refreshKey],
  )
  const rows = useRemote<PageData<T>>(request)
  return (
    <>
      {filters !== undefined && (
        <Space wrap className="page-filters">
          {filters}
        </Space>
      )}
      <ErrorAlert error={rows.error} />
      <Table<T>
        rowKey="id"
        scroll={TABLE_SCROLL}
        loading={rows.isLoading}
        columns={columns}
        dataSource={rows.data?.items ?? []}
        pagination={{
          current: page,
          pageSize,
          total: rows.data?.total ?? 0,
          onChange: (p, size) => {
            setPage(p)
            setPageSize(size)
          },
        }}
      />
    </>
  )
}
