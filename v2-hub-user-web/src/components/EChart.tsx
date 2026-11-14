import type { EChartsCoreOption, EChartsType } from 'echarts/core'
import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../i18n/i18n-context'

interface EChartProps {
  option: EChartsCoreOption
  height: number
  /** 图表的文字说明（读屏用）。 */
  ariaLabel: string
}

const RENDERER = 'svg'

/** ECharts 容器：首次挂载时才加载图表库；随容器尺寸重排，卸载时释放。加载失败显示一行提示。 */
export function EChart({ option, height, ariaLabel }: EChartProps) {
  const { t } = useI18n()
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
    return <div className="hub-empty">{t('chart.loadFailed')}</div>
  }
  return <div ref={hostRef} className="hub-echart" style={{ height }} role="img" aria-label={ariaLabel} />
}
