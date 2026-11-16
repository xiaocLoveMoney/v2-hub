import type { ProviderType } from '../../api/client'

/** 面板类型下拉框的选项；当前只有 3x-ui（接入服务器与 SSH 托管共用）。 */
export const PROVIDER_OPTIONS: { value: ProviderType; label: string }[] = [{ value: 'xui', label: '3x-ui（v3.x）' }]

/** 表单里默认选中的面板类型。 */
export const DEFAULT_PROVIDER: ProviderType = 'xui'
