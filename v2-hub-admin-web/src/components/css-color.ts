/**
 * 图表用到的设计变量及其兜底色：取值与 index.css 的 :root 一致，
 * 只在读不到 CSS 变量时（如测试环境、样式未加载）使用。
 */
const TOKEN_FALLBACKS = {
  '--hub-primary': '#6777ef',
  '--hub-info': '#3abaf4',
  '--hub-success': '#47c363',
  '--hub-warning': '#ffa426',
  '--hub-danger': '#fc544b',
  '--hub-body': '#6c757d',
  '--hub-chart-grid': '#e3eaef',
  '--hub-surface': '#fff',
} as const

/** 可以读给图表用的设计变量名。 */
export type ColorToken = keyof typeof TOKEN_FALLBACKS

/**
 * 读设计变量的实际颜色给 ECharts 用（它不认 CSS 变量）。
 * @param name 设计变量名
 * @returns 变量的计算值；为空时返回 index.css 里的同值兜底色
 */
export function tokenColor(name: ColorToken): string {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return value === '' ? TOKEN_FALLBACKS[name] : value
}
