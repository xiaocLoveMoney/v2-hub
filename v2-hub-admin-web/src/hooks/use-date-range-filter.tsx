import { DatePicker } from 'antd'
import type { Dayjs } from 'dayjs'
import { useState, type ReactNode } from 'react'

/** RangePicker 的取值：两端都可能没选。 */
type RangeValue = [Dayjs | null, Dayjs | null] | null

/** 按日期范围过滤的查询参数与筛选控件。 */
export interface DateRangeFilter {
  /** 起日零点（含）；没选为 undefined。 */
  from?: string
  /** 止日次日零点（不含）；没选为 undefined。 */
  to?: string
  /** 放进筛选栏的日期范围选择器。 */
  picker: ReactNode
}

/**
 * 列表的日期范围筛选：选的是整天，查询按半开区间 [起日零点, 止日次日零点) 传给接口，止日当天全部包含在内。
 * @returns from / to 用作查询参数（也作为 useCallback 的依赖），picker 放进筛选栏
 */
export function useDateRangeFilter(): DateRangeFilter {
  const [range, setRange] = useState<RangeValue>(null)
  const from = range?.[0]?.startOf('day').toISOString()
  const to = range?.[1]?.add(1, 'day').startOf('day').toISOString()
  const picker = <DatePicker.RangePicker allowClear aria-label="按时间范围过滤" value={range} onChange={(v) => setRange(v)} />
  return { from, to, picker }
}
