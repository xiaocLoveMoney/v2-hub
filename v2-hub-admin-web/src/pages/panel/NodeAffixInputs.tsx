import { Input, Space } from 'antd'
import { MAX_NODE_AFFIX_LEN, type NodeAffix } from './node-affix'

interface NodeAffixInputsProps {
  value: NodeAffix
  onChange: (next: NodeAffix) => void
}

/**
 * 节点显示名的前缀 / 后缀输入框（Ruling-80）；一键部署与批量改节点名共用。
 * @param value 当前的前后缀
 * @param onChange 任一输入框变化时调用
 */
export function NodeAffixInputs({ value, onChange }: NodeAffixInputsProps) {
  return (
    <Space.Compact className="full-width">
      <Space.Addon>前缀</Space.Addon>
      <Input
        aria-label="显示名前缀"
        placeholder="如 香港-"
        maxLength={MAX_NODE_AFFIX_LEN}
        value={value.prefix}
        onChange={(e) => onChange({ ...value, prefix: e.target.value })}
      />
      <Space.Addon>后缀</Space.Addon>
      <Input
        aria-label="显示名后缀"
        placeholder="如 -01"
        maxLength={MAX_NODE_AFFIX_LEN}
        value={value.suffix}
        onChange={(e) => onChange({ ...value, suffix: e.target.value })}
      />
    </Space.Compact>
  )
}
