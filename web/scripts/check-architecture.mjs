import { readdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import ts from 'typescript'
import { parse } from 'vue/compiler-sfc'

const codeFile = /\.(?:ts|tsx|vue)$/
const root = path.resolve(fileURLToPath(new URL('../src/', import.meta.url)))

function scripts(file, text) {
  if (!file.endsWith('.vue')) return [{ content: text, line: 0, kind: file.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS }]
  const { descriptor, errors } = parse(text, { filename: file })
  if (errors.length) throw new Error(file + ': ' + errors.map(String).join('; '))
  return [descriptor.script, descriptor.scriptSetup].filter(Boolean)
    .map(block => ({ content: block.content, line: block.loc.start.line - 1,
      kind: block.lang === 'tsx' || block.lang === 'jsx' ? ts.ScriptKind.TSX : ts.ScriptKind.TS }))
}

function dependencies(file, text) {
  const result = []
  for (const block of scripts(file, text)) {
    const source = ts.createSourceFile(file, block.content, ts.ScriptTarget.Latest, true, block.kind)
    if (source.parseDiagnostics.length) {
      const diagnostic = source.parseDiagnostics[0]
      const line = block.line + source.getLineAndCharacterOfPosition(diagnostic.start ?? 0).line + 1
      throw new Error(file + ':' + line + ': Cannot inspect dependencies in invalid syntax: '
        + ts.flattenDiagnosticMessageText(diagnostic.messageText, ' '))
    }
    function add(node, literal, typeOnly = false) {
      if (!literal || !ts.isStringLiteralLike(literal)) return
      result.push({ specifier: literal.text, typeOnly, line: block.line + source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1 })
    }
    function visit(node) {
      if (ts.isImportDeclaration(node)) {
        const clause = node.importClause
        const bindings = clause?.namedBindings
        const onlyNamedTypes = !clause?.name && bindings && ts.isNamedImports(bindings)
          && bindings.elements.length > 0 && bindings.elements.every(item => item.isTypeOnly)
        add(node, node.moduleSpecifier, !!clause?.isTypeOnly || !!onlyNamedTypes)
      } else if (ts.isExportDeclaration(node)) {
        const onlyNamedTypes = node.exportClause && ts.isNamedExports(node.exportClause)
          && node.exportClause.elements.length > 0 && node.exportClause.elements.every(item => item.isTypeOnly)
        add(node, node.moduleSpecifier, node.isTypeOnly || !!onlyNamedTypes)
      } else if (ts.isCallExpression(node) && node.expression.kind === ts.SyntaxKind.ImportKeyword) {
        add(node, node.arguments[0])
      } else if (ts.isImportTypeNode(node) && ts.isLiteralTypeNode(node.argument)) {
        add(node, node.argument.literal, true)
      }
      ts.forEachChild(node, visit)
    }
    visit(source)
  }
  return result
}

function resolveImport(file, specifier, sources) {
  if (!specifier.startsWith('.')) return null
  const target = path.posix.normalize(path.posix.join(path.posix.dirname(file), specifier.split('?')[0]))
  return [target, target + '.ts', target + '.tsx', target + '.vue', target + '/index.ts']
    .find(candidate => sources.has(candidate)) ?? null
}

/** Check actual imports, exports and literal dynamic imports, never comment text. */
export function checkArchitecture(sources) {
  const issues = []
  const graph = new Map([...sources.keys()].map(file => [file, []]))
  let importCount = 0
  for (const [file, content] of sources) {
    let imports
    try { imports = dependencies(file, content) }
    catch (error) { issues.push(error.message); continue }
    for (const dependency of imports) {
      const { specifier, line, typeOnly } = dependency
      const target = resolveImport(file, specifier, sources)
      const report = message => issues.push(file + ':' + line + ': ' + message)
      if (file.startsWith('api/') && (specifier === 'vue' || specifier.startsWith('vue/') || specifier.startsWith('@vue/')
        || target && !target.startsWith('api/'))) {
        report('API internals may only depend on API internals; move shared contracts down instead of importing ' + specifier)
      }
      if (file === 'api.ts' && target && !target.startsWith('api/')) {
        report('The compatibility facade may only assemble API modules: ' + specifier)
      }
      if (file.startsWith('features/') && target === 'api.ts') {
        report('Feature modules must depend on explicit API domain clients or types, not the legacy facade: ' + specifier)
      }
      if (/^features\/[^/]+\/(?:.*\/)?use[^/]*\.ts$/.test(file) && target?.endsWith('.vue')) {
        report('State composables must not depend on Vue components; pass a typed contract: ' + specifier)
      }
      const domain = /^features\/([^/]+)\//.exec(file)?.[1]
      const targetDomain = target && /^features\/([^/]+)\//.exec(target)?.[1]
      if (domain && domain !== 'workspace' && targetDomain && targetDomain !== domain
        && !(typeOnly && target === 'features/workspace/contracts.ts')) {
        report('Feature domains must not import another domain internals: ' + specifier)
      }
      if (target === 'App.vue' && file !== 'main.ts') report('Only the bootstrap entry may import the App composition root: ' + file)
      if (target && !typeOnly) { graph.get(file).push({ target, line }); importCount++ }
    }
  }
  const visited = new Set()
  const active = new Set()
  const trail = []
  function walk(file) {
    if (visited.has(file)) return
    active.add(file)
    trail.push(file)
    for (const edge of graph.get(file)) {
      if (active.has(edge.target)) {
        issues.push(file + ':' + edge.line + ': Runtime dependency cycle: '
          + [...trail.slice(trail.indexOf(edge.target)), edge.target].join(' -> '))
      } else walk(edge.target)
    }
    trail.pop()
    active.delete(file)
    visited.add(file)
  }
  for (const file of [...graph.keys()].sort()) walk(file)
  return { issues, files: sources.size, imports: importCount }
}

async function readSources(directory, prefix = '') {
  const sources = new Map()
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const relative = prefix + entry.name
    if (entry.isDirectory()) {
      for (const [name, content] of await readSources(path.join(directory, entry.name), relative + '/')) sources.set(name, content)
    } else if (codeFile.test(entry.name)) sources.set(relative, await readFile(path.join(directory, entry.name), 'utf8'))
  }
  return sources
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const result = checkArchitecture(await readSources(root))
  if (result.issues.length) {
    console.error(result.issues.join('\n'))
    process.exitCode = 1
  } else console.log('Architecture OK: ' + result.files + ' source modules, ' + result.imports + ' runtime imports; API, feature and cycle boundaries verified.')
}
