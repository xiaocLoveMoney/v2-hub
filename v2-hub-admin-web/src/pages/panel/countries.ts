/**
 * 服务器位置的国家码表（Ruling-65）：ISO 3166-1 alpha-2 正式分配的码，外加 IP 库常用的 XK（科索沃）。
 * 与后端 internal/manager/geoip 的码表保持一致；中文名由浏览器的 Intl.DisplayNames 生成。
 */
const COUNTRY_CODE_LIST =
  'AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ ' +
  'CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR ' +
  'GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP ' +
  'KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT ' +
  'MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW ' +
  'SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ ' +
  'UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS XK YE YT ZA ZM ZW'

export const COUNTRY_CODES: readonly string[] = COUNTRY_CODE_LIST.split(' ')

const DISPLAY_LOCALE = 'zh-CN'
const COUNTRY_SET = new Set(COUNTRY_CODES)
const regionNames = new Intl.DisplayNames([DISPLAY_LOCALE], { type: 'region', fallback: 'code' })

/** 国家码的中文名；不是码表里的码时原样返回。 */
export function countryName(code: string): string {
  if (!COUNTRY_SET.has(code)) {
    return code
  }
  return regionNames.of(code) ?? code
}

/** 列表里展示的位置：空串显示占位符。 */
export function formatCountry(code: string | null | undefined, placeholder = '—'): string {
  return code === null || code === undefined || code === '' ? placeholder : countryName(code)
}

/** 位置下拉框的选项，按中文名排序；label 带上两位码方便搜索。 */
export const COUNTRY_OPTIONS: readonly { value: string; label: string }[] = COUNTRY_CODES.map((code) => ({
  value: code,
  label: `${countryName(code)}（${code}）`,
})).sort((a, b) => a.label.localeCompare(b.label, DISPLAY_LOCALE))
