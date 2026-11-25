import { api } from '../../api/client'

// 代理页面共用的只读请求；代理界面只调 /api/admin/agent/* 与本人接口（Ruling-101）。模块级函数保持引用稳定，供 useRemote 使用

/** 我的配额与用量。 */
export const loadAgentSummary = (signal: AbortSignal) => api.GET('/api/admin/agent/summary', { signal })

/** 我可以分配的白名单套餐（含每次开通扣减的 poolCost）。 */
export const loadAgentPlans = (signal: AbortSignal) => api.GET('/api/admin/agent/plans', { signal })
