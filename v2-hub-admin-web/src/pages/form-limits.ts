// 多个页面表单共用的输入上限，与后端字段校验一致；只在一处使用的上限留在各自页面

/** 名称类字段（套餐、流量包、共享账号、出口）的最大长度。 */
export const MAX_NAME_LEN = 64

/** 备注字段的最大长度。 */
export const MAX_REMARK_LEN = 255

/** 邮箱地址的最大长度（RFC 5321）。 */
export const MAX_EMAIL_LEN = 254

/** 排序值的上限。 */
export const MAX_SORT_ORDER = 100000

/** 搜索关键字的最大长度。 */
export const MAX_KEYWORD_LEN = 64

/** 同时在线 IP 数的上限。 */
export const MAX_LIMIT_IP = 1000

/** 端口号的下限与上限。 */
export const MIN_PORT = 1
export const MAX_PORT = 65535

/** 管理员密码的长度下限与上限（docs/03 §4.2 密码）。 */
export const MIN_ADMIN_PASSWORD_LEN = 12
export const MAX_ADMIN_PASSWORD_LEN = 128

/** 两步验证码的位数与格式（docs/03 §4.3）。 */
export const TOTP_CODE_LEN = 6
export const TOTP_CODE_PATTERN = /^\d{6}$/
