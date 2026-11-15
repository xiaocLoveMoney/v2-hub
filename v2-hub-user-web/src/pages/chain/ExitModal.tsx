import { useState, type FormEvent } from 'react'
import { api, csrfHeader, type ChainExit } from '../../api/client'
import { Alert, Button, Field, Modal, Segmented, TextAreaField } from '../../components/ui'
import { mutate, request } from '../../hooks/use-remote'
import { useI18n } from '../../i18n/i18n-context'
import { ExitFieldsForm } from './ExitFieldsForm'
import { buildExitLink, emptyExitFields, exitFieldProblems, parseExitLink, type ExitFields } from './exit-link'
import { MAX_CHAIN_NAME_LEN, MAX_EXIT_LINK_LEN, isValidChainName } from './labels'

const LINK_ROWS = 5

/** 连接参数的两种填法（docs/06 §5 M9 出口弹窗）。 */
type FillMode = 'form' | 'link'
const MODE_FORM: FillMode = 'form'
const MODE_LINK: FillMode = 'link'

interface ExitModalProps {
  /** null = 添加；否则编辑这个出口。 */
  exit: ChainExit | null
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

/**
 * 添加或编辑我的出口（docs/06 §5 M9 出口弹窗）。连接参数有“表单 / 分享链接”两种填法，可来回切换，提交的始终是一条链接。
 * 编辑时默认不动连接参数；点“修改连接参数”才取一次现有链接填进来。链接与表单内容只留在本组件的内存里，关闭即丢弃。
 */
export function ExitModal({ exit, isOpen, onClose, onSaved }: ExitModalProps) {
  const { t } = useI18n()
  const isEditing = exit !== null
  const [name, setName] = useState(exit?.name ?? '')
  const [isRelinking, setIsRelinking] = useState(!isEditing)
  const [mode, setMode] = useState<FillMode>(MODE_FORM)
  const [fields, setFields] = useState<ExitFields>(emptyExitFields)
  const [link, setLink] = useState('')
  const [shouldShowProblems, setShouldShowProblems] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const [isLoadingLink, setIsLoadingLink] = useState(false)

  const trimmedName = name.trim()
  const problems = exitFieldProblems(fields)
  const isNameValid = isValidChainName(name) && (!isEditing || trimmedName !== '')

  /** 把一条链接放进表单；表单表达不了时留在链接方式并说明原因。 */
  const fillFromLink = (text: string) => {
    const parsed = parseExitLink(text)
    if (parsed === null) {
      setMode(MODE_LINK)
      setLink(text)
      setNotice(t('chain.linkUnparsable'))
      return
    }
    setFields(parsed.fields)
    setMode(MODE_FORM)
    setNotice(null)
    if (trimmedName === '') {
      setName(parsed.name)
    }
  }

  const switchMode = (next: FillMode) => {
    if (next === mode) {
      return
    }
    setNotice(null)
    if (next === MODE_LINK) {
      // 表单没填完时不覆盖已有的链接文本
      if (problems.size === 0) {
        setLink(buildExitLink(fields, trimmedName))
      }
      setMode(MODE_LINK)
      return
    }
    if (link.trim() === '') {
      setMode(MODE_FORM)
      return
    }
    fillFromLink(link.trim())
  }

  /** 编辑时点“修改连接参数”：取一次现有链接，填进表单。 */
  const startRelink = async () => {
    if (exit === null) {
      return
    }
    setIsLoadingLink(true)
    setError(null)
    try {
      const result = await request(() => api.GET('/api/user/chain/exits/{id}/link', { params: { path: { id: exit.id } } }))
      if (result.error !== null) {
        setError(result.error.message)
        return
      }
      fillFromLink(result.data.link)
      setIsRelinking(true)
    } finally {
      setIsLoadingLink(false)
    }
  }

  /** 这次要提交的链接：undefined = 不改连接参数；null = 还不能提交（缺项已标出或已提示）。 */
  const linkToSubmit = (): string | undefined | null => {
    if (!isRelinking) {
      return undefined
    }
    if (mode === MODE_FORM && problems.size > 0) {
      setShouldShowProblems(true)
      return null
    }
    const text = mode === MODE_FORM ? buildExitLink(fields, trimmedName) : link.trim()
    if (text === '') {
      setError(t('chain.linkRequired'))
      return null
    }
    if (text.length > MAX_EXIT_LINK_LEN) {
      setError(t('chain.linkTooLong', { max: MAX_EXIT_LINK_LEN }))
      return null
    }
    return text
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    if (!isNameValid) {
      return
    }
    const text = linkToSubmit()
    if (text === null) {
      return
    }
    setIsBusy(true)
    const err = await mutate(() => {
      if (exit === null) {
        return api.POST('/api/user/chain/exits', { params: csrfHeader(), body: { link: text ?? '', name: trimmedName } })
      }
      const body = text === undefined ? { name: trimmedName } : { name: trimmedName, link: text }
      return api.PUT('/api/user/chain/exits/{id}', { params: { ...csrfHeader(), path: { id: exit.id } }, body })
    })
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    onSaved()
  }

  const modes: { mode: FillMode; label: string }[] = [
    { mode: MODE_FORM, label: t('chain.modeForm') },
    { mode: MODE_LINK, label: t('chain.modeLink') },
  ]

  return (
    <Modal title={t(isEditing ? 'chain.editExit' : 'chain.addExit')} isOpen={isOpen} onClose={onClose}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Field
          label={t(isEditing ? 'chain.name' : 'chain.nameOptional')}
          autoComplete="off"
          maxLength={MAX_CHAIN_NAME_LEN}
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={isValidChainName(name) ? null : t('chain.badName', { max: MAX_CHAIN_NAME_LEN })}
          hint={isEditing ? undefined : t('chain.exitNameHint')}
        />
        {!isRelinking && (
          <div className="hub-exit-keep">
            <span className="hub-field-hint">{t('chain.paramsKept')}</span>
            <Button variant="outline" isBusy={isLoadingLink} onClick={() => void startRelink()}>
              {t('chain.changeParams')}
            </Button>
          </div>
        )}
        {isRelinking && (
          <>
            <Segmented
              aria="pressed"
              className="hub-exit-seg"
              aria-label={t('chain.modeLabel')}
              options={modes.map((m) => ({ value: m.mode, label: m.label }))}
              value={mode}
              onChange={switchMode}
            />
            {notice !== null && <Alert tone="info">{notice}</Alert>}
            {mode === MODE_FORM ? (
              <ExitFieldsForm fields={fields} onChange={setFields} problems={problems} shouldShowProblems={shouldShowProblems} />
            ) : (
              <TextAreaField
                label={t('chain.exitLink')}
                autoComplete="off"
                spellCheck={false}
                rows={LINK_ROWS}
                maxLength={MAX_EXIT_LINK_LEN}
                value={link}
                onChange={(e) => setLink(e.target.value)}
                hint={t('chain.linkHint')}
              />
            )}
            {mode === MODE_FORM && <p className="hub-field-hint">{t('chain.formHint')}</p>}
          </>
        )}
        <div className="hub-actions">
          <Button type="submit" variant="primary" isBusy={isBusy} disabled={!isNameValid}>
            {t('common.save')}
          </Button>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}
