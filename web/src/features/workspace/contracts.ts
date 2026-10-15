/** Public instance boundary between the workspace host and a feature module. */
export interface WorkspaceModuleHandle {
  /** Return false while saving or when the user declines to discard a draft. */
  canLeave?: () => boolean
  /** Optional chart drill-down; the host queues it while a lazy module loads. */
  showChart?: (id: string) => void
}
