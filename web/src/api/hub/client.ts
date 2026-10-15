import { get, send, post } from '../transport'
import type { Space, Room, Claim, Template, HealthOverlay, NodeInventoryView, NodeConfigFull, NodesResponse, ConsoleResponse } from './types'

/** Hub management always addresses nodes explicitly rather than through selection. */
export const hubApi = {
  nodes: (signal?: AbortSignal) => get<NodesResponse>('/api/v1/nodes', signal),
  forgetNode: (id: string) => send<void>('DELETE', `/api/v1/nodes?node=${encodeURIComponent(id)}`),
  spaces: () => get<{ spaces: Space[] }>('/api/v1/hub/spaces'),
  createSpace: (name: string) => post<Space>('/api/v1/hub/spaces', { name }),
  deleteSpace: (id: string) => send<void>('DELETE', `/api/v1/hub/spaces?id=${encodeURIComponent(id)}`),
  rooms: (spaceID = '') => get<{ rooms: Room[] }>(`/api/v1/hub/rooms${spaceID ? '?space_id=' + encodeURIComponent(spaceID) : ''}`),
  createRoom: (spaceID: string, name: string) => post<Room>('/api/v1/hub/rooms', { space_id: spaceID, name }),
  deleteRoom: (id: string) => send<void>('DELETE', `/api/v1/hub/rooms?id=${encodeURIComponent(id)}`),
  claims: () => get<{ claims: Claim[] }>('/api/v1/hub/claim-tokens'),
  issueClaim: (spaceID: string, roomID: string, ttl = '24h') =>
    post<Claim>('/api/v1/hub/claim-tokens', { space_id: spaceID, room_id: roomID, ttl }),
  nodeConfig: (node: string, signal?: AbortSignal) => get<NodeConfigFull>(`/api/v1/hub/config?node=${encodeURIComponent(node)}`, signal),
  putNodeConfig: (cfg: { node_id: string; yaml?: string; disabled?: string[]; if_updated?: number }) =>
    send<NodeConfigFull>('PUT', `/api/v1/hub/config?node=${encodeURIComponent(cfg.node_id)}`, cfg),
  console: () => get<ConsoleResponse>('/api/v1/hub/console'),
  templates: (signal?: AbortSignal) => get<{ templates: Template[] }>('/api/v1/hub/templates', signal),
  putTemplate: (tpl: Template, ifUpdated?: number) =>
    send<Template>('PUT', '/api/v1/hub/templates' + (tpl.id ? `?id=${encodeURIComponent(tpl.id)}` : ''), { ...tpl, if_updated: ifUpdated }),
  deleteTemplate: (id: string) => send<void>(`DELETE`, `/api/v1/hub/templates?id=${encodeURIComponent(id)}`),
  putTemplateRaw: (body: Record<string, unknown>, id?: string) =>
    send<Template>('PUT', '/api/v1/hub/templates' + (id ? `?id=${encodeURIComponent(id)}` : ''), body),
  templateEffective: (node: string, signal?: AbortSignal) =>
    get<HealthOverlay>(`/api/v1/hub/templates/effective?node=${encodeURIComponent(node)}`, signal),
  inventory: (signal?: AbortSignal) => get<{ inventory: Record<string, NodeInventoryView> }>('/api/v1/hub/inventory', signal),
  nodeInventory: (node: string, signal?: AbortSignal) =>
    get<NodeInventoryView>(`/api/v1/hub/inventory?node=${encodeURIComponent(node)}`, signal),
  putInventory: (node: string, fields: Record<string, string>, ifUpdated?: number) =>
    send<NodeInventoryView>(`PUT`, `/api/v1/hub/inventory?node=${encodeURIComponent(node)}`, { fields, if_updated: ifUpdated }),
}
