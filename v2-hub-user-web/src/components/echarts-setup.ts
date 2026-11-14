/**
 * ECharts 按需引入（docs/06 §5，Ruling-65）：只注册用到的图表、组件与 SVG 渲染器。
 * 本模块只经 EChart 组件动态 import，不进首屏包。
 */
import { LineChart, ScatterChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import * as echarts from 'echarts/core'
import { SVGRenderer } from 'echarts/renderers'

echarts.use([LineChart, ScatterChart, GridComponent, LegendComponent, TooltipComponent, SVGRenderer])

export { echarts }
