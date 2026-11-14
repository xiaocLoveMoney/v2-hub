/** 图标动效：none 静止；intro 出现时播一次出场；spin 加载中的步进旋转循环（docs/06 §4 图标动效）。 */
export type BrandMarkMotion = 'none' | 'intro' | 'spin'

// “三向聚光”图标的路径（坐标原点在图形中心，viewBox -60 -60 120 120，三重对称）
const VIEW_BOX = '-60 -60 120 120'
const BLADE = 'M0-52Q9-34 11-13L1-8L-4-16Q-1-34 0-52Z'
const DIAMOND = 'M0-5.5L5.5 0L0 5.5L-5.5 0Z'
/** 24px 以下用加粗版：刃更宽、中心菱形更大，小尺寸下更清楚。 */
const BLADE_BOLD = 'M0-56Q13-36 15-15L2-11L-7-19Q-3-36 0-56Z'
const DIAMOND_BOLD = 'M0-7L7 0L0 7L-7 0Z'
const BOLD_BELOW_PX = 24
const BLADE_ANGLES = [0, 120, 240] as const

interface BrandMarkProps {
  /** 像素尺寸；不给时为 1em，跟随字号（按小尺寸用加粗版）。 */
  size?: number
  motion?: BrandMarkMotion
  /** 悬停时步进旋转一次（品牌位用）。 */
  hasHoverTurn?: boolean
  className?: string
  /** 给了就作为有名称的图片，否则对读屏隐藏（旁边一般已有站点名）。 */
  label?: string
}

/** 平台图标（docs/06 §4 平台图标）：内联 SVG、单色 currentColor，默认主色；动效全部是 CSS，减少动态效果时不播。 */
export function BrandMark({ size, motion = 'none', hasHoverTurn = false, className = '', label }: BrandMarkProps) {
  const isBold = size === undefined || size < BOLD_BELOW_PX
  const dimension = size ?? '1em'
  const classes = ['hub-mark', `hub-mark-${motion}`, hasHoverTurn ? 'hub-mark-hover' : '', className].filter((c) => c !== '').join(' ')
  return (
    <svg
      className={classes}
      viewBox={VIEW_BOX}
      width={dimension}
      height={dimension}
      fill="currentColor"
      focusable="false"
      role={label === undefined ? undefined : 'img'}
      aria-label={label}
      aria-hidden={label === undefined ? true : undefined}
    >
      <g className="hub-mark-turn">
        <g className="hub-mark-core">
          {BLADE_ANGLES.map((angle, index) => (
            <g key={angle} transform={`rotate(${angle})`}>
              <path className={`hub-mark-blade hub-mark-blade-${index}`} d={isBold ? BLADE_BOLD : BLADE} />
            </g>
          ))}
          <path className="hub-mark-diamond" d={isBold ? DIAMOND_BOLD : DIAMOND} />
        </g>
      </g>
    </svg>
  )
}
