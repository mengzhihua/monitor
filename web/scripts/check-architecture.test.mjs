import assert from 'node:assert/strict'
import { test } from 'node:test'
import { checkArchitecture } from './check-architecture.mjs'

const check = files => checkArchitecture(new Map(Object.entries(files))).issues

test('the shell can compose domains while API internals remain independent', () => {
  assert.deepEqual(check({
    'main.ts': 'import App from "./App.vue"',
    'App.vue': '<script setup>import { api } from "./api"; import W from "./features/workspace/modules"</script>',
    'api.ts': 'export * from "./api/transport"',
    'api/transport.ts': 'export const api = {}',
    'features/workspace/modules.ts': 'export const loader = () => import("../operations/Panel.vue")',
    'features/operations/Panel.vue': '<script setup>import { api } from "../../api/transport"</script>',
  }), [])
})

test('API reverse imports and UI dependencies are rejected, including type-only imports', () => {
  const issues = check({
    'api.ts': 'export {}',
    'api/transport.ts': 'import type { api } from "../api"; import { ref } from "vue"; import C from "../components/C.vue"',
    'components/C.vue': '<template><p>view</p></template>',
  })
  assert.equal(issues.length, 3)
  assert.ok(issues.every(issue => issue.includes('API internals')))
})

test('composables receive contracts rather than importing component instances', () => {
  assert.match(check({
    'features/configuration/useEditor.ts': 'import type Editor from "../../components/Editor.vue"',
    'components/Editor.vue': '<template><div /></template>',
  })[0], /State composables/)
})

test('legacy components can use the facade while feature modules use explicit domain contracts', () => {
  const issues = check({
    'api.ts': 'export {}',
    'components/OldPanel.vue': '<script setup>import { api } from "../api"</script>',
    'features/operations/useOverview.ts': 'import { api } from "../../api"',
  })
  assert.equal(issues.length, 1)
  assert.match(issues[0], /explicit API domain/)
})

test('feature domains cannot reach another domain implementation or the App root', () => {
  const issues = check({
    'features/configuration/useEditor.ts': 'import "../operations/state"; import "../../App.vue"',
    'features/operations/state.ts': 'export {}',
    'App.vue': '<template><main /></template>',
  })
  assert.equal(issues.length, 3)
  assert.ok(issues.some(issue => issue.includes('another domain')))
  assert.ok(issues.some(issue => issue.includes('composition root')))
})

test('static, re-export and lazy edges all participate in cycle detection', () => {
  const issues = check({
    'a.ts': 'import "./b"',
    'b.ts': 'export * from "./c"',
    'c.ts': 'const next = () => import("./a")',
  })
  assert.equal(issues.length, 1)
  assert.match(issues[0], /a.ts -> b.ts -> c.ts -> a.ts/)
})

test('type-only edges do not create runtime cycles and mixed imports still do', () => {
  assert.deepEqual(check({
    'a.ts': 'import { type B } from "./b"',
    'b.ts': 'export type { A } from "./a"',
  }), [])
  assert.match(check({
    'a.ts': 'import { type B, run } from "./b"',
    'b.ts': 'export { run } from "./a"',
  })[0], /Runtime dependency cycle/)
})

test('comments and template strings are not mistaken for import declarations', () => {
  assert.deepEqual(check({
    'api/a.ts': '// import "../App.vue"\nconst text = \'import("../App.vue")\'',
    'App.vue': '<template><div>import "./App.vue"</div></template>',
  }), [])
})

test('Vue script-setup diagnostics point to the source line', () => {
  const issues = check({
    'features/operations/Card.vue': '<template><div /></template>\n<script setup lang="ts">\nimport "../../App.vue"\n</script>',
    'App.vue': '<template><div /></template>',
  })
  assert.match(issues[0], /^features\/operations\/Card.vue:3:/)
})

test('TypeScript generic arrows cannot hide later forbidden imports', () => {
  for (const [file, source] of [
    ['api/generic.ts', 'const identity = <T>(value: T) => value;\nimport App from "../App.vue"'],
    ['features/operations/Generic.vue', '<script setup lang="ts">\nconst identity = <T>(value: T) => value;\nimport App from "../../App.vue"\n</script>'],
  ]) {
    const issues = check({ [file]: source, 'App.vue': '<template><div /></template>' })
    assert.ok(issues.some(issue => issue.includes('composition root')), issues.join('\n'))
    assert.ok(!issues.some(issue => issue.includes('invalid syntax')))
  }
})

test('TSX modules and Vue TSX blocks still support JSX while syntax errors fail closed', () => {
  assert.deepEqual(check({
    'view.tsx': 'export const view = () => <div />',
    'Card.vue': '<script lang="tsx">export default { render: () => <div /> }</script>',
  }), [])
  assert.match(check({ 'broken.ts': 'export const =' })[0], /Cannot inspect dependencies in invalid syntax/)
})

test('API modules cannot bypass the UI boundary through Vue subpackages', () => {
  const issues = check({
    'api/leak.ts': 'import { ref } from "@vue/reactivity"; import { h } from "@vue/runtime-core"',
  })
  assert.equal(issues.length, 2)
  assert.ok(issues.every(issue => issue.includes('API internals')))
})

test('domain models accept API type contracts and other models without acquiring runtime services', () => {
  assert.deepEqual(check({
    'features/metrics/model/chartSeries.ts': 'import type { Chart } from "../../../api/monitoring/types"; import { limit } from "./bounds"',
    'features/metrics/model/bounds.ts': 'export const limit = 1200',
    'api/monitoring/types.ts': 'export interface Chart {}',
  }), [])
  const issues = check({
    'features/metrics/model/chartSeries.ts': 'import { ref } from "vue"; import "uplot"; import "../../../api/monitoring/client"; import "../chartRenderer"',
    'api/monitoring/client.ts': 'export {}',
    'features/metrics/chartRenderer.ts': 'export {}',
  })
  assert.equal(issues.length, 4)
  assert.ok(issues.every(issue => issue.includes('Domain models')))
})

test('type-only imports cannot smuggle component or orchestration contracts into domain models', () => {
  const issues = check({
    'features/metrics/model/chartSeries.ts': 'import type Card from "../../../components/Card.vue"; import type { Session } from "../useSession"',
    'components/Card.vue': '<template><div /></template>',
    'features/metrics/useSession.ts': 'export interface Session {}',
  })
  assert.equal(issues.length, 2)
  assert.ok(issues.every(issue => issue.includes('Domain models')))
})
