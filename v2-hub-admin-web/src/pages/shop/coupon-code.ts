// 随机码：10 位，字符集同邀请码（去掉易混的 0O1IL，docs/03 §6，Ruling-63）
const RANDOM_CODE_LEN = 10
const RANDOM_CODE_ALPHABET = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789'

/** 浏览器里生成随机优惠码（crypto.getRandomValues，不用 Math.random）。 */
export function randomCouponCode(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(RANDOM_CODE_LEN))
  return Array.from(bytes, (b) => RANDOM_CODE_ALPHABET[b % RANDOM_CODE_ALPHABET.length]).join('')
}
