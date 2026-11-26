/** 流量工具：接口一律按字节传（hub-api PlanInput.trafficLimit 等），界面上按 GB 填写与显示。放在 src/ 下，api/ 与 pages/ 都能引用。 */

/** 1 GB 的字节数。 */
export const BYTES_PER_GB = 1024 ** 3
const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const
const BYTE_STEP = 1024
/** GB 的小数位：显示、回显与输入框共用，所以回显值与原字节数会有舍入差。 */
export const GB_PRECISION = 2

/**
 * 字节数格式化为人读的大小。
 * @param bytes 字节数
 * @returns 如 “100.00 GB”；不足 1 KB 时不带小数
 */
export function formatBytes(bytes: number): string {
  let value = bytes
  let unit = 0
  while (value >= BYTE_STEP && unit < BYTE_UNITS.length - 1) {
    value /= BYTE_STEP
    unit++
  }
  return `${unit === 0 ? value : value.toFixed(GB_PRECISION)} ${BYTE_UNITS[unit]}`
}

/**
 * GB → 字节（四舍五入到整数字节）。
 * @param gb 表单里填的 GB；空按 0
 */
export function gbToBytes(gb: number | null | undefined): number {
  return Math.round((gb ?? 0) * BYTES_PER_GB)
}

/**
 * 字节 → GB，保留 GB_PRECISION 位小数（给表单回显用）。
 * @param bytes 字节数
 */
export function bytesToGb(bytes: number): number {
  return Number((bytes / BYTES_PER_GB).toFixed(GB_PRECISION))
}
