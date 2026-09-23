// Keep nearby canvases for quick back-scrolling without retaining every chart
// ever visited. Active charts are never part of this small offscreen cache.
const idle = new Set<() => void>()
const limit = 8

export function retainChart(release: () => void) {
  idle.delete(release)
  idle.add(release)
  while (idle.size > limit) {
    const oldest = idle.values().next().value!
    idle.delete(oldest)
    oldest()
  }
}

export function activateChart(release: () => void) { idle.delete(release) }
