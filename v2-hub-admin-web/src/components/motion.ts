import { gsap } from 'gsap'

/**
 * 页面进场动效（GSAP）。弹窗、抽屉、消息提示由 antd 自带动效，这里只管页面与登录卡片的进场。
 * 用户系统设置了“减少动态效果”或环境不支持 matchMedia 时不做动画；测试的 matchMedia 桩对该查询返回 true（src/test/setup.ts）。
 */
const MOTION_REVEAL = 0.42
const EASE_ENTER = 'power2.out'

/** 进场时元素自下而上移动的距离（px）。 */
const REVEAL_OFFSET_PX = 16
/** 相邻元素依次进场的间隔（秒），以及整组间隔的上限，元素多时按上限均摊。 */
const REVEAL_STAGGER_EACH = 0.06
const REVEAL_STAGGER_MAX_TOTAL = 0.36
/** 动画结束后清掉的行内样式，避免 transform 残留影响 position: fixed 的后代。 */
const CLEAR_MOTION_PROPS = 'opacity,visibility,transform'

const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)'

/** 当前是否做动画：浏览器支持 matchMedia 且用户没有要求减少动态效果。 */
export function isMotionEnabled(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return false
  }
  return !window.matchMedia(REDUCED_MOTION_QUERY).matches
}

/** 一组元素依次淡入上移。 */
function revealElements(targets: Element[]) {
  if (targets.length === 0) {
    return
  }
  const each = Math.min(REVEAL_STAGGER_EACH, REVEAL_STAGGER_MAX_TOTAL / targets.length)
  gsap.fromTo(
    targets,
    { autoAlpha: 0, y: REVEAL_OFFSET_PX },
    { autoAlpha: 1, y: 0, duration: MOTION_REVEAL, ease: EASE_ENTER, stagger: each, overwrite: true, clearProps: CLEAR_MOTION_PROPS },
  )
}

/**
 * ref 回调：容器的直接子元素在首次挂载与之后每次插入时依次进场。
 * 路由切换（Outlet 换成新页面）和数据加载完成（“加载中”换成正文）都会插入新的子元素，因此一处接线即可覆盖。
 * MutationObserver 的回调在本轮任务的微任务里执行，先于浏览器绘制，新元素不会先闪一下再隐藏。
 * 用法：`<main ref={revealChildren}>`；依赖 React 19 的 ref 回调清理函数。
 */
export function revealChildren(root: HTMLElement | null): (() => void) | undefined {
  if (root === null || !isMotionEnabled()) {
    return undefined
  }
  const ctx = gsap.context(() => {
    revealElements(Array.from(root.children))
  }, root)
  const observer = new MutationObserver((records) => {
    const added = records.flatMap((r) => Array.from(r.addedNodes)).filter((n): n is Element => n instanceof Element && n.parentElement === root)
    ctx.add(() => revealElements(added))
  })
  observer.observe(root, { childList: true })
  return () => {
    observer.disconnect()
    ctx.revert()
  }
}
