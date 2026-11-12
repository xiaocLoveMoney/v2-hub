import { DeleteOutlined, PlusOutlined } from '@ant-design/icons'
import { Button, Form, Input, InputNumber, Select, Space, Switch } from 'antd'
import type { ReactNode } from 'react'
import { fieldId, getAt, type InboundDraft, type JsonObject, type Path } from './model'

/** 表单字段读写草稿的入口：所有字段按路径读写同一个 InboundDraft。 */
export interface Binder {
  draft: InboundDraft
  set: (path: Path, value: unknown) => void
  /** 整体变换草稿（换协议 / 传输 / 安全层时按规则重置相关段落）。 */
  update: (change: (d: InboundDraft) => InboundDraft) => void
}

interface FieldProps {
  b: Binder
  path: Path
  label: ReactNode
  extra?: ReactNode
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function strList(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
}

export function TextField({ b, path, label, extra, placeholder, action }: FieldProps & { placeholder?: string; action?: ReactNode }) {
  const input = <Input id={fieldId(path)} value={str(getAt(b.draft, path))} placeholder={placeholder} autoComplete="off" onChange={(e) => b.set(path, e.target.value)} />
  return (
    <Form.Item label={label} extra={extra} htmlFor={fieldId(path)}>
      {action === undefined ? (
        input
      ) : (
        <Space.Compact className="full-width">
          {input}
          {action}
        </Space.Compact>
      )}
    </Form.Item>
  )
}

export function NumberField({ b, path, label, extra, min, max }: FieldProps & { min?: number; max?: number }) {
  const v = getAt(b.draft, path)
  return (
    <Form.Item label={label} extra={extra} htmlFor={fieldId(path)}>
      <InputNumber
        id={fieldId(path)}
        className="full-width"
        min={min}
        max={max}
        value={typeof v === 'number' ? v : null}
        onChange={(next) => b.set(path, typeof next === 'number' ? next : undefined)}
      />
    </Form.Item>
  )
}

export function SwitchField({ b, path, label, extra }: FieldProps) {
  return (
    <Form.Item label={label} extra={extra} htmlFor={fieldId(path)}>
      <Switch id={fieldId(path)} checked={getAt(b.draft, path) === true} onChange={(next) => b.set(path, next)} />
    </Form.Item>
  )
}

export function SelectField({ b, path, label, extra, options, onChange }: FieldProps & {
  options: readonly (string | { value: string; label: ReactNode })[]
  onChange?: (value: string) => void
}) {
  const opts = options.map((o) => (typeof o === 'string' ? { value: o, label: o } : o))
  return (
    <Form.Item label={label} extra={extra} htmlFor={fieldId(path)}>
      <Select id={fieldId(path)} value={str(getAt(b.draft, path))} options={opts} onChange={(v: string) => (onChange === undefined ? b.set(path, v) : onChange(v))} />
    </Form.Item>
  )
}

/** 字符串数组：可选值固定时多选，否则回车添加任意值。 */
export function TagsField({ b, path, label, extra, options, action }: FieldProps & { options?: readonly string[]; action?: ReactNode }) {
  const select = (
    <Select
      id={fieldId(path)}
      mode={options === undefined ? 'tags' : 'multiple'}
      open={options === undefined ? false : undefined}
      value={strList(getAt(b.draft, path))}
      options={options?.map((o) => ({ value: o, label: o }))}
      onChange={(v: string[]) => b.set(path, v)}
      tokenSeparators={[',', ' ']}
    />
  )
  return (
    <Form.Item label={label} extra={extra} htmlFor={fieldId(path)}>
      {action === undefined ? (
        select
      ) : (
        <Space.Compact className="full-width">
          {select}
          {action}
        </Space.Compact>
      )}
    </Form.Item>
  )
}

/** 字符串数组，每行一项（证书内容、PEM 等）。 */
export function LinesField({ b, path, label, extra, rows = 4 }: FieldProps & { rows?: number }) {
  return (
    <Form.Item label={label} extra={extra} htmlFor={fieldId(path)}>
      <Input.TextArea
        id={fieldId(path)}
        rows={rows}
        className="mono"
        value={strList(getAt(b.draft, path)).join('\n')}
        onChange={(e) => b.set(path, e.target.value === '' ? [] : e.target.value.split('\n'))}
      />
    </Form.Item>
  )
}

/** 键值对象（请求头、端口映射等）。valueAsList 为 true 时值是字符串数组（RAW 的 HTTP 伪装头）。 */
export function KeyValueField({ b, path, label, extra, valueAsList = false }: FieldProps & { valueAsList?: boolean }) {
  const obj = (getAt(b.draft, path) ?? {}) as JsonObject
  const rows = Object.entries(obj).map(([k, v]) => [k, valueAsList ? strList(v).join(',') : str(v)] as const)
  const write = (next: (readonly [string, string])[]) => {
    const out: JsonObject = {}
    for (const [k, v] of next) {
      out[k] = valueAsList ? v.split(',').filter((x) => x !== '') : v
    }
    b.set(path, out)
  }
  return (
    <Form.Item label={label} extra={extra}>
      <Space orientation="vertical" className="full-width">
        {rows.map(([k, v], i) => (
          <Space.Compact key={i} className="full-width">
            <Input placeholder="名称" value={k} onChange={(e) => write(rows.map((r, j) => (j === i ? [e.target.value, r[1]] : r)))} />
            <Input placeholder="值" value={v} onChange={(e) => write(rows.map((r, j) => (j === i ? [r[0], e.target.value] : r)))} />
            <Button icon={<DeleteOutlined />} aria-label="删除" onClick={() => write(rows.filter((_, j) => j !== i))} />
          </Space.Compact>
        ))}
        <Button size="small" icon={<PlusOutlined />} onClick={() => write([...rows, [`key${rows.length + 1}`, '']])}>
          添加
        </Button>
      </Space>
    </Form.Item>
  )
}

export interface ListColumn {
  key: string
  label: string
  type?: 'text' | 'number'
}

/** 对象数组（回落、账号等），每项一行。 */
export function ObjectListField({ b, path, label, extra, columns, newItem }: FieldProps & { columns: ListColumn[]; newItem: () => JsonObject }) {
  const list = Array.isArray(getAt(b.draft, path)) ? (getAt(b.draft, path) as JsonObject[]) : []
  return (
    <Form.Item label={label} extra={extra}>
      <Space orientation="vertical" className="full-width">
        {list.map((item, i) => (
          <Space.Compact key={i} className="full-width">
            {columns.map((c) =>
              c.type === 'number' ? (
                <InputNumber
                  key={c.key}
                  placeholder={c.label}
                  value={typeof item[c.key] === 'number' ? (item[c.key] as number) : null}
                  onChange={(v) => b.set([...path, i, c.key], typeof v === 'number' ? v : 0)}
                />
              ) : (
                <Input key={c.key} placeholder={c.label} value={String(item[c.key] ?? '')} onChange={(e) => b.set([...path, i, c.key], e.target.value)} />
              ),
            )}
            <Button icon={<DeleteOutlined />} aria-label="删除" onClick={() => b.set(path, list.filter((_, j) => j !== i))} />
          </Space.Compact>
        ))}
        <Button size="small" icon={<PlusOutlined />} onClick={() => b.set(path, [...list, newItem()])}>
          添加
        </Button>
      </Space>
    </Form.Item>
  )
}
