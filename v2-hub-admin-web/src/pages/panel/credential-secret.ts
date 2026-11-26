import { Form, type FormInstance } from 'antd'
import { useState } from 'react'
import type { SshAuthType } from '../../api/client'

// 密码凭据的确认（Ruling-97）：各主机弹窗共用的常量、请求体拼装与接线 hook；输入栏组件在 CredentialSecretField.tsx

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

/** 编辑已有主机时，判断（凭据，地址，端口）是否变化所需的原值。 */
interface CredentialTarget {
  credentialId: number
  address: string
  sshPort: number
}

/** 表单里与密码确认相关的字段（各主机弹窗的表单都含其中一部分）。 */
interface CredentialSecretFields {
  credentialId?: number
  address?: string
  sshPort?: number
  credentialSecret?: string
}

/** 主机弹窗里密码确认的接线。 */
export interface CredentialSecretState {
  /** 当前是否要显示并提交“再次输入该凭据的密码”。 */
  isNeeded: boolean
  /** 交给 SshCredentialSelect 的 onAuthTypeChange（引用稳定）。 */
  onAuthTypeChange: (authType: SshAuthType | null) => void
  /** 提交（或测试连接）后清空输入的密码，不留在表单里。 */
  clear: () => void
  /** 请求体里的 credentialSecret 部分，见 credentialSecretPart。 */
  bodyPart: (value: string | undefined) => { credentialSecret?: string }
}

/**
 * 主机弹窗的密码确认（Ruling-97，docs/03 §5.22 密码凭据的确认）：“何时需要再输一次凭据密码”只在这里判断——
 * 凭据是密码类型时需要；传了 original（编辑已有主机）则还要（凭据，地址，端口）与原值不同才需要。
 * @param form 弹窗的表单实例
 * @param original 编辑时的原（凭据，地址，端口）；新建、测试连接、绑定 SSH 不传，总是要求
 */
export function useCredentialSecret<T extends CredentialSecretFields>(form: FormInstance<T>, original?: CredentialTarget): CredentialSecretState {
  const [authType, setAuthType] = useState<SshAuthType | null>(null)
  // 三个弹窗的表单类型各不相同、都只含这些字段的一部分；这里只按字段名读写，收窄到公共字段
  const watched = form as unknown as FormInstance<CredentialSecretFields>
  const credentialId = Form.useWatch('credentialId', watched)
  const address = Form.useWatch('address', watched)
  const sshPort = Form.useWatch('sshPort', watched)
  const isTargetChanged =
    original === undefined ||
    (credentialId ?? original.credentialId) !== original.credentialId ||
    (address ?? original.address).trim() !== original.address ||
    (sshPort ?? original.sshPort) !== original.sshPort
  const isNeeded = authType === PASSWORD_AUTH_TYPE && isTargetChanged
  return {
    isNeeded,
    onAuthTypeChange: setAuthType,
    clear: () => watched.setFieldValue(CREDENTIAL_SECRET_FIELD, undefined),
    bodyPart: (value) => credentialSecretPart(isNeeded, value),
  }
}
