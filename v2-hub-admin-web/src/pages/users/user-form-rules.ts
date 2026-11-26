import { MAX_EMAIL_LEN } from '../form-limits'

// 终端用户表单的校验：员工的用户弹窗与代理的名下用户弹窗共用（docs/03 §5.3、§5.8、§5.23）

// 终端用户密码 8–128 位（docs/03 §5.3；管理员密码是 12 位起）
const MIN_CREDENTIAL_LEN = 8
const MAX_CREDENTIAL_LEN = 128
/** 新用户名不含 @，好与邮箱登录区分（docs/03 §5.8）。 */
export const USERNAME_PATTERN = /^[A-Za-z0-9_.-]{3,64}$/
/** 邮箱输入框的校验（新建与编辑用户共用，代理侧同样）。 */
export const emailRules = [{ type: 'email' as const, max: MAX_EMAIL_LEN, message: '邮箱格式不对' }]

/** 终端用户密码输入框的校验（新建、重置密码共用，代理侧同样）。 */
export const credentialRules = [
  { required: true, message: '请输入密码' },
  { min: MIN_CREDENTIAL_LEN, max: MAX_CREDENTIAL_LEN, message: `密码长度须为 ${MIN_CREDENTIAL_LEN}–${MAX_CREDENTIAL_LEN} 个字符` },
]
