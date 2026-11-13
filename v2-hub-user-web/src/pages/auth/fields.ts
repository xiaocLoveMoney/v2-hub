/** 登录、注册、找回口令与账号页共用的输入约束（取值与 hub-api.yaml 一致）。 */

/** 邮箱验证码是 6 位数字（docs/03 §5.7）。 */
export const EMAIL_CODE_LENGTH = 6
export const TOTP_LENGTH = 6
export const MAX_EMAIL_LEN = 254
export const MIN_CREDENTIAL_LEN = 8
export const MAX_CREDENTIAL_LEN = 128
export const MAX_USERNAME_LEN = 64
export const MAX_INVITE_CODE_LEN = 16
/** 邀请链接里带邀请码的查询参数名；注册页读它，邀请页用它拼链接。 */
export const INVITE_QUERY_KEY = 'code'
/** 新用户名：3–64 位字母、数字或 _ . -，不含 @（docs/03 §5.8）。 */
export const USERNAME_PATTERN = /^[A-Za-z0-9_.-]{3,64}$/

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
const NON_DIGITS = /\D/g

/** 粗校验邮箱格式，只用来决定按钮能不能点；最终以后端校验为准。 */
export function isEmailLike(email: string): boolean {
  return email.length <= MAX_EMAIL_LEN && EMAIL_PATTERN.test(email)
}

/** 只保留数字（验证码输入框用）。 */
export function digitsOnly(value: string): string {
  return value.replace(NON_DIGITS, '')
}
