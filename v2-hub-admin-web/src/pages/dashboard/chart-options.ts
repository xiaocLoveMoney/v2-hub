import type { EChartsCoreOption } from 'echarts/core'
import { tokenColor, type ColorToken } from '../../components/css-color'

/** 图表配置的生成（docs/05 总览分析）：全站图表统一一根纵轴、细线细柱、悬停出数值，两个系列时带图例。 */

const LINE_WIDTH = 2
const BAR_MAX_WIDTH = 22
const BAR_RADIUS = 4
const STACK_GAP = 1
const GRID_TOP_WITH_LEGEND = 36
const GRID_TOP = 16
const STACK_NAME = 'total'

/** 一个数据系列；color 取设计变量。values 里的 null 表示这个点没有数据（折线在此断开）。 */
export interface ChartSeries {
  name: string
  values: (number | null)[]
  color: ColorToken
}

interface ChartInput {
  /** 横轴类目（日期、小时等），与各系列的 values 等长。 */
  labels: string[]
  series: ChartSeries[]
  /** 纵轴刻度与悬停数值的格式化。 */
  format: (value: number) => string
}

function frame({ labels, series, format }: ChartInput): EChartsCoreOption {
  const hasLegend = series.length > 1
  const muted = tokenColor('--hub-body')
  const gridColor = tokenColor('--hub-chart-grid')
  return {
    grid: { left: 8, right: 16, top: hasLegend ? GRID_TOP_WITH_LEGEND : GRID_TOP, bottom: 8, containLabel: true },
    legend: { show: hasLegend, top: 0, right: 0, icon: 'roundRect', textStyle: { color: muted } },
    tooltip: { trigger: 'axis', valueFormatter: (v: unknown) => format(typeof v === 'number' ? v : 0) },
    xAxis: { type: 'category', data: labels, axisTick: { show: false }, axisLine: { lineStyle: { color: gridColor } }, axisLabel: { color: muted } },
    yAxis: {
      type: 'value',
      minInterval: 1,
      axisLabel: { color: muted, formatter: (v: number) => format(v) },
      splitLine: { lineStyle: { color: gridColor } },
    },
  }
}

/** 折线图：2px 线、不画数据点，悬停时按横轴出各系列的数值。 */
export function lineOption(input: ChartInput): EChartsCoreOption {
  return {
    ...frame(input),
    series: input.series.map((s) => ({
      name: s.name,
      type: 'line',
      data: s.values,
      showSymbol: false,
      lineStyle: { width: LINE_WIDTH, color: tokenColor(s.color) },
      itemStyle: { color: tokenColor(s.color) },
    })),
  }
}

/** 柱状图；isStacked 为真时各系列叠在一根柱上，段与段之间留一条底色缝，只有最上面一段是圆角。 */
export function barOption(input: ChartInput, isStacked = false): EChartsCoreOption {
  const surface = tokenColor('--hub-surface')
  const last = input.series.length - 1
  return {
    ...frame(input),
    series: input.series.map((s, i) => ({
      name: s.name,
      type: 'bar',
      data: s.values,
      stack: isStacked ? STACK_NAME : undefined,
      barMaxWidth: BAR_MAX_WIDTH,
      itemStyle: {
        color: tokenColor(s.color),
        borderRadius: !isStacked || i === last ? [BAR_RADIUS, BAR_RADIUS, 0, 0] : 0,
        borderColor: surface,
        borderWidth: isStacked ? STACK_GAP : 0,
      },
    })),
  }
}
