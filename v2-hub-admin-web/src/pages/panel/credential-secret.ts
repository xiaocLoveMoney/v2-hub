import type { SshAuthType } from '../../api/client'

// 密码凭据的确认（Ruling-97）：各主机弹窗共用的常量与请求体拼装；输入栏组件在 CredentialSecretField.tsx

/** 需要再次输入密码的凭据认证方式（私钥不会发给对端，不需要，Ruling-97）。 */
export const PASSWORD_AUTH_TYPE: SshAuthType = 'password'

/** 表单里“再次输入该凭据的密码”的字段名，与请求体的 credentialSecret 同名。 */
export const CREDENTIAL_SECRET_FIELD = 'credentialSecret'

/**
 * 请求体里的 credentialSecret 部分：需要时带上输入的值，不需要时不带这个字段。
 * @param isNeeded 是否需要再次输入（凭据是密码类型且命中 docs/03 §5.22“密码凭据的确认”的情形）
 * @param value 表单里输入的值
 */
export function credentialSecretPart(isNeeded: boolean, value: string | undefined): { credentialSecret?: string } {
  return isNeeded ? { credentialSecret: value ?? '' } : {}
}
