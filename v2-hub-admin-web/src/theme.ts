import type { ThemeConfig } from 'antd'

/**
 * 界面配色与尺寸（Ruling-21）：取值与 docs/06 §4 的设计变量一致，两端观感统一。
 * 同名的 CSS 变量定义在 index.css 的 :root，样式表里一律用变量，不写字面色值。
 */
export const PALETTE = {
  PRIMARY: '#6777ef',
  PRIMARY_HOVER: '#394eea',
  PRIMARY_GLOW: '#acb5f6',
  PRIMARY_LIGHT: '#e4e6fc',
  SUCCESS: '#47c363',
  DANGER: '#fc544b',
  WARNING: '#ffa426',
  INFO: '#3abaf4',
  PAGE_BG: '#f4f6f9',
  HEADING: '#34395e',
  BODY: '#6c757d',
  DIVIDER: '#e3eaef',
  TABLE_HEAD_BG: '#fafbfe',
} as const

const FONT_FAMILY = "'Nunito Variable', 'Nunito', 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', arial, sans-serif"
const BORDER_RADIUS = 3
const PRIMARY_SHADOW = `0 2px 6px ${PALETTE.PRIMARY_GLOW}`
const CARD_SHADOW = '0 4px 8px rgba(0, 0, 0, 0.03)'

/** antd 主题：只改 token，不改组件结构，页面代码不需要跟着动。 */
export const adminTheme: ThemeConfig = {
  token: {
    colorPrimary: PALETTE.PRIMARY,
    colorInfo: PALETTE.INFO,
    colorSuccess: PALETTE.SUCCESS,
    colorWarning: PALETTE.WARNING,
    colorError: PALETTE.DANGER,
    colorLink: PALETTE.PRIMARY,
    colorBgLayout: PALETTE.PAGE_BG,
    colorTextBase: PALETTE.HEADING,
    colorTextSecondary: PALETTE.BODY,
    colorBorderSecondary: PALETTE.DIVIDER,
    borderRadius: BORDER_RADIUS,
    fontFamily: FONT_FAMILY,
    boxShadowTertiary: CARD_SHADOW,
  },
  components: {
    Button: { fontWeight: 600, primaryShadow: PRIMARY_SHADOW, dangerShadow: 'none', defaultShadow: 'none' },
    Layout: { siderBg: '#ffffff', headerBg: 'transparent', bodyBg: PALETTE.PAGE_BG, headerHeight: 70, headerPadding: '0 24px' },
    Menu: {
      itemSelectedBg: '#f8fafb',
      itemSelectedColor: PALETTE.PRIMARY,
      itemColor: '#78828a',
      itemHoverColor: PALETTE.PRIMARY,
      itemBorderRadius: 0,
      itemMarginInline: 0,
      itemHeight: 46,
      groupTitleColor: '#868e96',
      activeBarBorderWidth: 0,
    },
    Table: { headerBg: PALETTE.TABLE_HEAD_BG, headerColor: PALETTE.HEADING, headerSplitColor: 'transparent', borderColor: PALETTE.DIVIDER },
    Card: { headerFontSize: 16 },
    Modal: { borderRadiusLG: BORDER_RADIUS },
  },
}
