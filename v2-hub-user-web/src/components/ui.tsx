import { useEffect, useId, type ButtonHTMLAttributes, type CSSProperties, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react'
import { useI18n } from '../i18n/i18n-context'

/** 设计系统的基础组件（docs/06 §4）。只负责外观与无障碍语义，不含业务逻辑。 */

type ButtonVariant = 'default' | 'primary' | 'danger' | 'outline' | 'ghost'

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  isBlock?: boolean
  isLarge?: boolean
  isBusy?: boolean
}

/** 按钮；isBusy 时禁用，防止重复提交。 */
export function Button({ variant = 'default', isBlock = false, isLarge = false, isBusy = false, className = '', disabled, type = 'button', children, ...rest }: ButtonProps) {
  const classes = ['hub-btn', variant === 'default' ? '' : `hub-btn-${variant}`, isBlock ? 'hub-btn-block' : '', isLarge ? 'hub-btn-lg' : '', className]
  return (
    <button type={type} className={classes.filter((c) => c !== '').join(' ')} disabled={disabled === true || isBusy} aria-busy={isBusy} {...rest}>
      {children}
    </button>
  )
}

interface CardProps {
  title?: string
  extra?: ReactNode
  children: ReactNode
  className?: string
}

/** 白底卡片；有 title 时带卡片头。 */
export function Card({ title, extra, children, className = '' }: CardProps) {
  return (
    <section className={`hub-card ${className}`.trim()}>
      {title !== undefined && (
        <header className="hub-card-header">
          <h4>{title}</h4>
          {extra}
        </header>
      )}
      <div className="hub-card-body">{children}</div>
    </section>
  )
}

type Tone = 'primary' | 'success' | 'danger' | 'warning' | 'info'

/** 药丸形徽章。 */
export function Badge({ tone, children }: { tone?: Tone | 'light'; children: ReactNode }) {
  return <span className={`hub-badge${tone === undefined ? '' : ` hub-badge-${tone}`}`}>{children}</span>
}

/** 横幅提示；plain 用于表单内的错误说明。 */
export function Alert({ tone, children }: { tone: 'danger' | 'warning' | 'info' | 'plain'; children: ReactNode }) {
  return (
    <div className={`hub-alert hub-alert-${tone}`} role={tone === 'info' ? 'status' : 'alert'}>
      {children}
    </div>
  )
}

interface StatCardProps {
  icon: ReactNode
  tone: Tone
  label: string
  value: ReactNode
  foot: ReactNode
}

/** 统计卡：左侧彩色图标块 + 标题 + 大数字 + 底部说明行。 */
export function StatCard({ icon, tone, label, value, foot }: StatCardProps) {
  const style = { '--stat-color': `var(--hub-${tone})`, '--stat-glow': `var(--hub-${tone}-glow)` } as CSSProperties
  return (
    <div className="hub-stat" style={style}>
      <div className="hub-stat-icon" aria-hidden="true">
        {icon}
      </div>
      <div className="hub-stat-label">{label}</div>
      <div className="hub-stat-value">{value}</div>
      <div className="hub-stat-foot">{foot}</div>
    </div>
  )
}

interface FieldProps extends InputHTMLAttributes<HTMLInputElement> {
  label: string
  error?: string | null
  hint?: string
}

/** 带标签的输入框；error 非空时显示在输入框下方。 */
export function Field({ label, error, hint, id, ...rest }: FieldProps) {
  const autoId = useId()
  const inputId = id ?? autoId
  const hasError = error !== undefined && error !== null && error !== ''
  return (
    <div className="hub-field">
      <label className="hub-field-label" htmlFor={inputId}>
        {label}
      </label>
      <input id={inputId} className="hub-input" aria-invalid={hasError} {...rest} />
      {hasError && <div className="hub-field-error">{error}</div>}
      {!hasError && hint !== undefined && <div className="hub-field-hint">{hint}</div>}
    </div>
  )
}

interface TextAreaFieldProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  label: string
  hint?: string
}

/** 带标签的多行输入框。 */
export function TextAreaField({ label, hint, id, ...rest }: TextAreaFieldProps) {
  const autoId = useId()
  const inputId = id ?? autoId
  return (
    <div className="hub-field">
      <label className="hub-field-label" htmlFor={inputId}>
        {label}
      </label>
      <textarea id={inputId} className="hub-input hub-textarea" {...rest} />
      {hint !== undefined && <div className="hub-field-hint">{hint}</div>}
    </div>
  )
}

interface SelectFieldProps extends SelectHTMLAttributes<HTMLSelectElement> {
  label: string
  hint?: string
  children: ReactNode
}

/** 带标签的下拉框；选项由调用方以 option / optgroup 传入。 */
export function SelectField({ label, hint, id, children, ...rest }: SelectFieldProps) {
  const autoId = useId()
  const inputId = id ?? autoId
  return (
    <div className="hub-field">
      <label className="hub-field-label" htmlFor={inputId}>
        {label}
      </label>
      <select id={inputId} className="hub-input" {...rest}>
        {children}
      </select>
      {hint !== undefined && <div className="hub-field-hint">{hint}</div>}
    </div>
  )
}

interface PagerProps {
  page: number
  pageSize: number
  total: number
  onChange: (page: number) => void
}

/** 上一页 / 下一页；只有一页时不渲染。 */
export function Pager({ page, pageSize, total, onChange }: PagerProps) {
  const { t } = useI18n()
  if (total <= pageSize) {
    return null
  }
  const pageCount = Math.max(Math.ceil(total / pageSize), 1)
  return (
    <div className="hub-pagination">
      <Button variant="outline" disabled={page <= 1} onClick={() => onChange(page - 1)}>
        {t('common.prevPage')}
      </Button>
      <span>
        {page} / {pageCount}
      </span>
      <Button variant="outline" disabled={page >= pageCount} onClick={() => onChange(page + 1)}>
        {t('common.nextPage')}
      </Button>
    </div>
  )
}

interface ModalProps {
  title: string
  isOpen: boolean
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
}

/** 模态框：点遮罩或按 Esc 关闭。关闭时不渲染，内部状态随之重置。 */
export function Modal({ title, isOpen, onClose, children, footer }: ModalProps) {
  const titleId = useId()
  useEffect(() => {
    if (!isOpen) {
      return undefined
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [isOpen, onClose])

  if (!isOpen) {
    return null
  }
  return (
    <div
      className="hub-modal-mask"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) {
          onClose()
        }
      }}
    >
      <div className="hub-modal" role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <header className="hub-modal-header">
          <h4 id={titleId}>{title}</h4>
        </header>
        <div className="hub-modal-body">{children}</div>
        {footer !== undefined && <footer className="hub-modal-footer">{footer}</footer>}
      </div>
    </div>
  )
}

interface ConfirmProps {
  title: string
  isOpen: boolean
  isBusy?: boolean
  confirmText: string
  onConfirm: () => void
  onClose: () => void
  children: ReactNode
}

/** 危险操作的二次确认（docs/06 §3 不变式 5）。 */
export function Confirm({ title, isOpen, isBusy = false, confirmText, onConfirm, onClose, children }: ConfirmProps) {
  const { t } = useI18n()
  return (
    <Modal
      title={title}
      isOpen={isOpen}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button variant="danger" isBusy={isBusy} onClick={onConfirm}>
            {confirmText}
          </Button>
        </>
      }
    >
      {children}
    </Modal>
  )
}

/** 页头：标题 + 右侧操作。 */
export function PageHeader({ title, extra }: { title: string; extra?: ReactNode }) {
  return (
    <div className="hub-page-header">
      <h1>{title}</h1>
      {extra}
    </div>
  )
}

/** 空状态。 */
export function Empty({ children }: { children: ReactNode }) {
  return <div className="hub-empty">{children}</div>
}
