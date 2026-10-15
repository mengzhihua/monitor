/** Compatibility facade. New domain implementations import their own API modules. */
export { auth } from './api/auth'
export { selection } from './api/selection'
export { ApiError, get, send } from './api/transport'

export type * from './api/monitoring/types'
export type * from './api/alarms/types'
export type * from './api/operations/types'
export type * from './api/configuration/types'
export type * from './api/hub/types'

import { monitoringApi } from './api/monitoring/client'
import { alarmsApi } from './api/alarms/client'
import { operationsApi } from './api/operations/client'
import { configurationApi } from './api/configuration/client'
import { hubApi } from './api/hub/client'

/** Stable public API; domain clients remain independently importable. */
export const api = {
  ...monitoringApi,
  ...alarmsApi,
  ...operationsApi,
  ...configurationApi,
  ...hubApi,
}
