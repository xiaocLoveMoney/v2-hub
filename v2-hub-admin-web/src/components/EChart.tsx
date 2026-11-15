import type { EChartsCoreOption, EChartsType } from 'echarts/core'
import { useEffect, useRef, useState } from 'react'

interface EChartProps {
  option: EChartsCoreOption
  /** 图表高度（像素）。 */
  height: number
  /** 图表的文字说明（读屏用）。 */
  ariaLabel: string
}

const RENDERER = 'svg'
const LOAD_FAILED = '图表加载失败，请刷新重试'

/** ECharts 容器：首次挂载时才加载图表库；随容器尺寸重排，卸载时释放。加载失败显示一行提示。 */
export function EChart({ option, height, ariaLabel }: EChartProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<EChartsType | null>(null)
  const optionRef = useRef(option)
  const [hasFailed, setHasFailed] = useState(false)

  useEffect(() => {
    let isDisposed = false
    let observer: ResizeObserver | undefined
    import('./echarts-setup')
      .then(({ echarts }) => {
        const host = hostRef.current
        if (isDisposed || host === null) {
          return
        }
        const chart = echarts.init(host, undefined, { renderer: RENDERER })
        chart.setOption(optionRef.current)
        chartRef.current = chart
        if (typeof ResizeObserver !== 'undefined') {
          observer = new ResizeObserver(() => chart.resize())
          observer.observe(host)
        }
      })
      .catch((err: unknown) => {
        reportError(err)
        if (!isDisposed) {
          setHasFailed(true)
        }
      })
    return () => {
      isDisposed = true
      observer?.disconnect()
      chartRef.current?.dispose()
      chartRef.current = null
    }
  }, [])

  useEffect(() => {
    optionRef.current = option
    chartRef.current?.setOption(option, { notMerge: true })
  }, [option])

  if (hasFailed) {
    return <div className="chart-empty">{LOAD_FAILED}</div>
  }
  return <div ref={hostRef} className="chart-host" style={{ height }} role="img" aria-label={ariaLabel} />
}
