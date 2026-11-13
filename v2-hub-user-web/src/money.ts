/** 金额工具：接口里一律是整数分（docs/03 §5.12），前端只在显示与表单输入时换成“元”，不做金额运算。 */

const CENTS_PER_UNIT = 100
const MONEY_FRACTION_DIGITS = 2
export const DEFAULT_CURRENCY = 'CNY'
const DEFAULT_TAG = 'zh-CN'

/** 把分格式化成带币种符号的金额；币种只影响显示，不做换算；tag 是界面语言标签（docs/06 §5 M8 的做法）。 */
export function formatMoney(cents: number, currency: string = DEFAULT_CURRENCY, tag: string = DEFAULT_TAG): string {
  return new Intl.NumberFormat(tag, {
    style: 'currency', currency, minimumFractionDigits: MONEY_FRACTION_DIGITS, maximumFractionDigits: MONEY_FRACTION_DIGITS,
  }).format(cents / CENTS_PER_UNIT)
}

/** 表单里按“元”输入的金额换成整数分；不是合法金额时返回 null。四舍五入到分，避免浮点尾数进到接口里。 */
export function parseAmountToCents(input: string): number | null {
  const value = Number(input)
  if (input.trim() === '' || !Number.isFinite(value) || value <= 0) {
    return null
  }
  return Math.round(value * CENTS_PER_UNIT)
}
