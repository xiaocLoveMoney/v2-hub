/** 金额工具：接口与库里一律是整数分（docs/03 §5.12），只在显示与表单输入时换成“元”。 */

const CENTS_PER_UNIT = 100
/** 表单里金额输入框的小数位数。 */
export const MONEY_PRECISION = 2
/** 单价与单笔金额上限（分），与 hub-api.yaml 的 Money 一致。 */
export const MAX_MONEY_CENTS = 99_999_999
/** 金额输入框的上限（元）。 */
export const MAX_MONEY_AMOUNT = MAX_MONEY_CENTS / CENTS_PER_UNIT
export const DEFAULT_CURRENCY = 'CNY'

/** 分 → 元（给表单输入框回显用）。 */
export function centsToAmount(cents: number): number {
  return cents / CENTS_PER_UNIT
}

/** 元 → 分：四舍五入到整数分，避免 0.1 + 0.2 式的浮点尾数进到接口里。 */
export function amountToCents(amount: number): number {
  return Math.round(amount * CENTS_PER_UNIT)
}

/** 把分格式化成带币种符号的金额；币种只影响显示，不做换算。 */
export function formatMoney(cents: number, currency: string = DEFAULT_CURRENCY): string {
  return new Intl.NumberFormat('zh-CN', { style: 'currency', currency, minimumFractionDigits: MONEY_PRECISION, maximumFractionDigits: MONEY_PRECISION }).format(
    centsToAmount(cents),
  )
}
