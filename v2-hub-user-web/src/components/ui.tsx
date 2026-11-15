import { useEffect, useId, useRef, useState, type ButtonHTMLAttributes, type CSSProperties, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react'
import { createPortal } from 'react-dom'
import { useI18n } from '../i18n/i18n-context'
import { EASE_ENTER, EASE_LEAVE, EASE_POP, MOTION_BASE, MOTION_FAST, gsap, isMotionEnabled, useGSAP } from './motion'
import { BrandMark } from './BrandMark'

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

/**
 * 分段选择的无障碍写法：radio = radiogroup + radio + aria-checked，tab = tablist + tab + aria-selected，
 * pressed = group + 切换按钮 aria-pressed。各站点沿用自己原有的写法。
 */
export type SegmentedAria = 'radio' | 'tab' | 'pressed'

const SEGMENTED_GROUP_ROLES: Record<SegmentedAria, string> = {
  radio: 'radiogroup',
  tab: 'tablist',
  pressed: 'group',
}

/** 分段选择的一项。 */
export interface SegmentedOption<T extends string | number> {
  value: T
  label: ReactNode
}

interface SegmentedProps<T extends string | number> {
  options: readonly SegmentedOption<T>[]
  value: T
  onChange: (value: T) => void
  aria: SegmentedAria
  /** 追加在 hub-seg 后的类名。 */
  className?: string
  'aria-label'?: string
  'aria-labelledby'?: string
}

/** 每项按钮的 role 与选中态属性。 */
function segmentedItemAria(aria: SegmentedAria, isActive: boolean) {
  if (aria === 'radio') {
    return { role: 'radio', 'aria-checked': isActive }
  }
  if (aria === 'tab') {
    return { role: 'tab', 'aria-selected': isActive }
  }
  return { 'aria-pressed': isActive }
}

/** 分段选择块（hub-seg）：一排互斥的按钮，选中项加 is-active；option 的 value 同时作 key，须唯一。 */
export function Segmented<T extends string | number>({ options, value, onChange, aria, className, 'aria-label': ariaLabel, 'aria-labelledby': ariaLabelledBy }: SegmentedProps<T>) {
  return (
    <div className={className === undefined ? 'hub-seg' : `hub-seg ${className}`} role={SEGMENTED_GROUP_ROLES[aria]} aria-label={ariaLabel} aria-labelledby={ariaLabelledBy}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          {...segmentedItemAria(aria, option.value === value)}
          className={`hub-seg-item${option.value === value ? ' is-active' : ''}`}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
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

/** 退场动画期间仍要显示的内容：关闭后父组件往往已清空数据，沿用最后一次打开时的内容。 */
interface ModalView {
  title: string
  children: ReactNode
  footer?: ReactNode
}

/** 弹窗进场时自下而上移动的距离（px）与起始缩放。 */
const MODAL_OFFSET_PX = 24
const MODAL_START_SCALE = 0.96

/**
 * 模态框：点遮罩或按 Esc 关闭，挂到 body 下以免被祖先的 transform 影响定位。
 * 打开时遮罩淡入、对话框上浮；关闭时先播退场动画再卸载，卸载后内部状态随之重置。不做动画时（见 motion.ts）关闭即卸载。
 */
export function Modal({ title, isOpen, onClose, children, footer }: ModalProps) {
  const titleId = useId()
  const maskRef = useRef<HTMLDivElement>(null)
  const [isPresent, setIsPresent] = useState(isOpen)
  // 记住打开期间最后一次的内容（React 文档“保存上一次渲染的信息”的写法），退场时沿用
  const [shownView, setShownView] = useState<ModalView>({ title, children, footer })
  const isViewStale = shownView.title !== title || shownView.children !== children || shownView.footer !== footer
  if (isOpen && isViewStale) {
    setShownView({ title, children, footer })
  }
  if (isOpen && !isPresent) {
    setIsPresent(true)
  }
  if (!isOpen && isPresent && !isMotionEnabled()) {
    setIsPresent(false)
  }

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

  useGSAP(
    () => {
      const mask = maskRef.current
      const dialog = mask?.firstElementChild ?? null
      if (!isPresent || mask === null || dialog === null || !isMotionEnabled()) {
        return
      }
      if (isOpen) {
        gsap.fromTo(mask, { autoAlpha: 0 }, { autoAlpha: 1, duration: MOTION_BASE, ease: EASE_ENTER, overwrite: true })
        gsap.fromTo(
          dialog,
          { y: MODAL_OFFSET_PX, scale: MODAL_START_SCALE },
          { y: 0, scale: 1, duration: MOTION_BASE, ease: EASE_POP, overwrite: true, clearProps: 'transform' },
        )
        return
      }
      gsap.to(dialog, { y: MODAL_OFFSET_PX / 2, scale: MODAL_START_SCALE, duration: MOTION_FAST, ease: EASE_LEAVE, overwrite: true })
      gsap.to(mask, { autoAlpha: 0, duration: MOTION_FAST, ease: EASE_LEAVE, overwrite: true, onComplete: () => setIsPresent(false) })
    },
    { dependencies: [isOpen, isPresent], scope: maskRef },
  )

  if (!isPresent) {
    return null
  }
  const view = isOpen ? { title, children, footer } : shownView
  return createPortal(
    <div
      ref={maskRef}
      className={`hub-modal-mask${isOpen ? '' : ' is-leaving'}`}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) {
          onClose()
        }
      }}
    >
      <div className="hub-modal" role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <header className="hub-modal-header">
          <h4 id={titleId}>{view.title}</h4>
        </header>
        <div className="hub-modal-body">{view.children}</div>
        {view.footer !== undefined && <footer className="hub-modal-footer">{view.footer}</footer>}
      </div>
    </div>,
    document.body,
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

interface DataTableProps {
  /** 表头各列的内容，按列顺序。 */
  headers: readonly ReactNode[]
  /** 表体的行（tr 列表）。 */
  children: ReactNode
  /** 数据已取到且为空时为 true，表格下方显示空状态。 */
  isEmpty: boolean
  emptyText: ReactNode
}

/** 横向可滚动的表格（hub-table-wrap + hub-table），为空时在表格下方显示空状态。 */
export function DataTable({ headers, children, isEmpty, emptyText }: DataTableProps) {
  return (
    <>
      <div className="hub-table-wrap">
        <table className="hub-table">
          <thead>
            <tr>
              {/* 表头列固定不增删，按位置作 key */}
              {headers.map((header, index) => (
                <th key={index}>{header}</th>
              ))}
            </tr>
          </thead>
          <tbody>{children}</tbody>
        </table>
      </div>
      {isEmpty && <Empty>{emptyText}</Empty>}
    </>
  )
}

const LOADING_MARK_BLOCK = 40
const LOADING_MARK_INLINE = 18

/** 加载中：图标步进旋转 + 文字（docs/06 §4 图标动效）；inline 用于卡片、弹窗内的小块区域，默认占满内容区。 */
export function Loading({ isInline = false }: { isInline?: boolean }) {
  const { t } = useI18n()
  return (
    <div className={`hub-loading${isInline ? ' hub-loading-inline' : ''}`} role="status">
      <BrandMark size={isInline ? LOADING_MARK_INLINE : LOADING_MARK_BLOCK} motion="spin" />
      <span>{t('common.loading')}</span>
    </div>
  )
}
