import { MAX_ADMIN_PASSWORD_LEN, MIN_ADMIN_PASSWORD_LEN } from '../form-limits'

// 管理端账号（管理员、代理）表单的校验：管理员页、代理页与本人改密码共用（docs/03 §4.2 密码、§5.23）

/** 管理端用户名：3–64 位字母、数字或 _ . -；管理员与代理共用一个命名空间。 */
const ADMIN_USERNAME_PATTERN = /^[a-zA-Z0-9_.-]{3,64}$/

/** 用户名输入框的校验（新建管理员、新建代理）。 */
export const adminUsernameRules = [{ required: true, pattern: ADMIN_USERNAME_PATTERN, message: '3–64 位字母、数字或 _ . -' }]

/** 管理端密码的长度规则（12–128 位）；必填提示各处文案不同，由调用方另配。 */
export const ADMIN_CREDENTIAL_LENGTH_RULE = {
  min: MIN_ADMIN_PASSWORD_LEN,
  max: MAX_ADMIN_PASSWORD_LEN,
  message: `密码长度须为 ${MIN_ADMIN_PASSWORD_LEN}–${MAX_ADMIN_PASSWORD_LEN} 个字符`,
}

/** 管理端账号密码输入框的校验（初始密码、超管重置密码）。 */
export const adminCredentialRules = [{ required: true, message: '请输入密码' }, ADMIN_CREDENTIAL_LENGTH_RULE]
