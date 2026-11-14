/** 读设计变量的实际颜色给 ECharts 用（它不认 CSS 变量）。 */
export function cssColor(name: string, fallback: string): string {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return value === '' ? fallback : value
}
