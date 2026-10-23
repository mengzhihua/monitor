import { computed, onBeforeUnmount, onMounted, ref, shallowRef, triggerRef, watch, type Ref } from 'vue'
import type { Chart } from '../../api/monitoring/types'
import { monitoringApi } from '../../api/monitoring/client'
import { selection } from '../../api/selection'
import { live } from '../../live'
import { pageVisible } from '../../visibility'
import { activateChart, retainChart } from '../../chart_cache'
import { createChartRenderer } from './chartRenderer'
import { appendLiveSample, chartDefinition, collectionStep, emptySeries, historySeries } from './model/chartSeries'

interface MetricChartSource { chart: Chart; window: number; detail?: boolean }
interface ChartElements { card: Ref<HTMLDivElement | undefined>; el: Ref<HTMLDivElement | undefined> }

/** A chart's single owner for accepted data, requests and visible-resource lifetime. */
export function useMetricChart(props: MetricChartSource, anchor: Ref<number | null>, zoomed: Ref<boolean>, { card, el }: ChartElements) {
  const series = shallowRef(emptySeries())
  const snapshotReady = ref(false)
  const loading = ref(false)
  const error = ref('')
  const clock = ref(Date.now() / 1000)
  const lastSample = ref(props.chart.last_entry || 0)
  const stale = computed(() => !lastSample.value || clock.value - lastSample.value > Math.max(15, 3 * collectionStep(props.chart)))
  const anomalous = computed(() => Object.values(series.value.anomaly).some(value => value >= 50))
  const sampleCount = computed(() => series.value.times.length)
  const sampleStep = computed(() => series.value.sampleStep)
  const dataNode = computed(() => series.value.node)

  let disposed = false
  let visible = false
  let inViewport = false
  let generation = 0
  let request: AbortController | undefined
  let refreshTimer: number | undefined
  let renderFrame: number | undefined
  let unsubscribe: (() => void) | null = null
  let intersection: IntersectionObserver | undefined
  let resize: ResizeObserver | undefined
  const renderer = createChartRenderer({
    element: () => el.value,
    chart: () => props.chart,
    series: () => series.value,
    historical: () => anchor.value !== null,
    height: () => props.detail ? Math.max(240, Math.min(560, window.innerHeight * 0.55)) : 180,
  })

  function release() {
    renderer.release()
    series.value = emptySeries()
    snapshotReady.value = false
  }

  function stopWork() {
    ++generation
    request?.abort()
    request = undefined
    loading.value = false
    clearTimeout(refreshTimer)
    if (renderFrame !== undefined) cancelAnimationFrame(renderFrame)
    renderFrame = undefined
  }

  function scheduleRefresh(first: boolean) {
    clearTimeout(refreshTimer)
    if (!visible || disposed || anchor.value !== null) return
    refreshTimer = window.setTimeout(() => {
      clock.value = Date.now() / 1000
      void load()
    }, first ? refreshOffset(props.chart.id) : 30000)
  }

  async function load(first = false) {
    if (!visible || disposed) return
    clearTimeout(refreshTimer)
    request?.abort()
    const current = request = new AbortController()
    const expected = ++generation
    error.value = ''
    snapshotReady.value = false
    loading.value = true
    const requestedNode = selection.node || 'local'
    const requestedChart = props.chart
    try {
      if (anchor.value !== null && anchor.value - props.window < 1) throw new Error('此结束时间无法容纳当前看板时长，请选择更晚的结束时间。')
      // One bucket per collection period also preserves slow charts' isolated samples.
      const response = await monitoringApi.data(requestedChart.id,
        anchor.value === null ? -props.window : anchor.value - props.window, anchor.value ?? 0,
        Math.min(1200, Math.ceil(props.window / collectionStep(requestedChart))), current.signal)
      if (disposed || current.signal.aborted || expected !== generation) return
      series.value = historySeries(requestedChart, response, requestedNode)
      snapshotReady.value = true
    } catch (failure) {
      if (disposed || current.signal.aborted || expected !== generation) return
      error.value = String(failure)
      release()
    }
    request = undefined
    loading.value = false
    renderer.render()
    scheduleRefresh(first)
  }

  function onLive(timestamp: number, values: Record<string, number>) {
    if (anchor.value !== null) return
    lastSample.value = Math.max(lastSample.value, timestamp)
    if (!appendLiveSample(series.value, timestamp, values, props.window, collectionStep(props.chart))) return
    triggerRef(series)
    if (renderFrame === undefined) {
      renderFrame = requestAnimationFrame(() => {
        renderFrame = undefined
        if (!disposed) renderer.update()
      })
    }
  }

  function updateSubscription() {
    unsubscribe?.()
    unsubscribe = visible && anchor.value === null && props.window <= 1200
      ? live.subscribe(props.chart.id, message => onLive(message.t, message.v)) : null
  }

  function updateVisibility() {
    const active = inViewport && pageVisible.value && !zoomed.value
    if (active === visible) return
    visible = active
    updateSubscription()
    if (active) {
      activateChart(release)
      clock.value = Date.now() / 1000
      void load(true)
    } else {
      stopWork()
      if (renderer.exists()) retainChart(release)
    }
  }

  onMounted(() => {
    intersection = new IntersectionObserver(entries => {
      inViewport = entries.some(entry => entry.isIntersecting)
      updateVisibility()
    }, { rootMargin: '300px 0px' })
    intersection.observe(card.value!)
    resize = new ResizeObserver(() => { if (visible) renderer.resize() })
    resize.observe(el.value!)
  })
  onBeforeUnmount(() => {
    disposed = true
    stopWork()
    intersection?.disconnect()
    unsubscribe?.()
    resize?.disconnect()
    activateChart(release)
    release()
  })
  watch([pageVisible, zoomed], updateVisibility, { flush: 'sync' })
  watch(() => props.chart.last_entry, timestamp => { lastSample.value = Math.max(lastSample.value, timestamp || 0) })
  watch([() => props.window, () => chartDefinition(props.chart, anchor.value !== null), anchor], () => {
    stopWork()
    release()
    updateSubscription()
    if (visible) void load()
  })

  return { series, snapshotReady, loading, error, lastSample, stale, anomalous, sampleCount, sampleStep, dataNode, load }
}

/** Spread periodic history queries across 30 seconds, avoiding one burst per dashboard. */
function refreshOffset(id: string) {
  let hash = 2166136261
  for (let index = 0; index < id.length; index++) hash = Math.imul(hash ^ id.charCodeAt(index), 16777619)
  return 1000 + (hash >>> 0) % 29000
}
