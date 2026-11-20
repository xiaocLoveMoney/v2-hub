import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { useI18n } from '../i18n/i18n-context'

const KEY_ESCAPE = 'Escape'
/** 按钮上已选项名称之间的分隔；中英文界面通用。 */
const SUMMARY_SEPARATOR = ', '

/** 下拉多选里的一个选项。 */
export interface MultiSelectOption {
  value: number
  label: string
  /** 不可选；reason 写明原因，显示在选项后面。 */
  isDisabled?: boolean
  reason?: string
}

interface MultiSelectProps {
  label: string
  /** 没选任何项时按钮上显示的文字。 */
  placeholder: string
  options: MultiSelectOption[]
  value: number[]
  onChange: (value: number[]) => void
  /** 最多选几个；达到后其余选项置灰。省略 = 不限。 */
  max?: number
  hint?: string
}

/**
 * 下拉多选（docs/06 §5 M9）：按钮展开一组复选框，点外面或按 Esc 收起。
 * 选项是原生复选框，键盘可达；按钮上显示已选项的名称，放不下时由样式截断。
 * 面板顶部有“全选 / 取消全选”（Ruling-87）：全选只加可选的项，取消全选清空。
 */
export function MultiSelect({ label, placeholder, options, value, onChange, max, hint }: MultiSelectProps) {
  const { t } = useI18n()
  const [isOpen, setIsOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)
  const labelId = useId()
  const panelId = useId()

  // 展开期间监听文档上的按下：点在组件外面就收起；收起或卸载时解除监听
  useEffect(() => {
    if (!isOpen) {
      return undefined
    }
    const handlePointerDown = (e: PointerEvent) => {
      if (rootRef.current !== null && e.target instanceof Node && !rootRef.current.contains(e.target)) {
        setIsOpen(false)
      }
    }
    document.addEventListener('pointerdown', handlePointerDown)
    return () => document.removeEventListener('pointerdown', handlePointerDown)
  }, [isOpen])

  const selected = new Set(value)
  const isFull = max !== undefined && value.length >= max
  const selectable = options.filter((o) => o.isDisabled !== true)
  const isAllSelected = selectable.length > 0 && selectable.every((o) => selected.has(o.value))
  const summary = options
    .filter((o) => selected.has(o.value))
    .map((o) => o.label)
    .join(SUMMARY_SEPARATOR)

  const toggle = (option: MultiSelectOption) => {
    // 保持选项原有的顺序，不按点击先后
    const next = options.filter((o) => (o.value === option.value ? !selected.has(o.value) : selected.has(o.value))).map((o) => o.value)
    onChange(next)
  }

  const toggleAll = () => {
    if (isAllSelected) {
      onChange([])
      return
    }
    // 已选的项（哪怕现在置灰）保留，再补上所有可选的；按选项原有顺序，超过上限的截掉
    const next = options.filter((o) => selected.has(o.value) || o.isDisabled !== true).map((o) => o.value)
    onChange(max === undefined ? next : next.slice(0, max))
  }

  const handleKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === KEY_ESCAPE && isOpen) {
      e.stopPropagation()
      setIsOpen(false)
      buttonRef.current?.focus()
    }
  }

  return (
    <div className="hub-field hub-multi" ref={rootRef} onKeyDown={handleKeyDown}>
      <span className="hub-field-label" id={labelId}>
        {label}
      </span>
      <button
        ref={buttonRef}
        type="button"
        className={`hub-input hub-multi-button${summary === '' ? ' is-empty' : ''}`}
        aria-haspopup="true"
        aria-expanded={isOpen}
        aria-controls={panelId}
        aria-labelledby={labelId}
        onClick={() => setIsOpen((v) => !v)}
      >
        <span className="hub-multi-summary">{summary === '' ? placeholder : summary}</span>
        <span className="hub-multi-caret" aria-hidden="true">
          ▾
        </span>
      </button>
      {isOpen && (
        <div className="hub-multi-panel" id={panelId} role="group" aria-labelledby={labelId}>
          <div className="hub-multi-toolbar">
            <span className="hub-field-hint">{t('common.selectedCount', { count: value.length, total: options.length })}</span>
            <button type="button" className="hub-multi-all" disabled={selectable.length === 0} onClick={toggleAll}>
              {isAllSelected ? t('common.deselectAll') : t('common.selectAll')}
            </button>
          </div>
          {options.map((o) => {
            const isChecked = selected.has(o.value)
            const isLocked = o.isDisabled === true || (isFull && !isChecked)
            return (
              <label key={o.value} className={`hub-multi-option${isLocked ? ' is-disabled' : ''}`}>
                <input type="checkbox" checked={isChecked} disabled={isLocked} onChange={() => toggle(o)} />
                <span>{o.label}</span>
                {o.reason !== undefined && <span className="hub-field-hint">（{o.reason}）</span>}
              </label>
            )
          })}
        </div>
      )}
      {hint !== undefined && <div className="hub-field-hint">{hint}</div>}
    </div>
  )
}
