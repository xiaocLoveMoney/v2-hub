// jsdom 没有 ResizeObserver，antd Typography 的 ellipsis 依赖它；测试里不需要真实尺寸变化。
if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

const REDUCED_MOTION_FEATURE = 'prefers-reduced-motion'

// jsdom 没有 matchMedia，antd 的响应式组件（Sider breakpoint、Grid）依赖它。
// 对“减少动态效果”返回 true：测试里不做页面进场动画，元素渲染后立即可见（components/motion.ts）。
if (typeof window !== 'undefined' && typeof window.matchMedia !== 'function') {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: (query: string): MediaQueryList => ({
      matches: query.includes(REDUCED_MOTION_FEATURE),
      media: query,
      onchange: null,
      addListener: () => undefined,
      removeListener: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      dispatchEvent: () => false,
    }),
  })
}
