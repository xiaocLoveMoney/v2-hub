import { Space, Tag } from 'antd'
import type { Server } from '../../api/client'
import { EMPTY } from './host-labels'

/** 一台服务器上大陆连通检测的概况：有被墙或部分不通的地址就标出个数；否则说明有几个地址测出来是正常的。 */
export function ServerReachCell({ reach }: { reach: Server['reach'] }) {
  if (reach === null || reach.total === 0) {
    return <>{EMPTY}</>
  }
  if (reach.blocked === 0 && reach.partial === 0) {
    if (reach.ok === 0) {
      // 没有一个地址测出正常：还没测、未知或端口不通，细节在节点页
      return <Tag>暂无结论</Tag>
    }
    return <Tag color="green">{reach.ok === reach.total ? '正常' : `正常 ${reach.ok}/${reach.total}`}</Tag>
  }
  return (
    <Space size={4}>
      {reach.blocked > 0 && <Tag color="red">被墙 {reach.blocked}</Tag>}
      {reach.partial > 0 && <Tag color="orange">部分不通 {reach.partial}</Tag>}
    </Space>
  )
}
