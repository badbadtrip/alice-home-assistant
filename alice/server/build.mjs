// One file instead of node_modules: the app image stays small and starts without a transpiler.
import { build } from 'esbuild'

const shims = [
  "import { createRequire as __aliceRequire } from 'node:module'",
  "import { fileURLToPath as __alicePath } from 'node:url'",
  "import { dirname as __aliceDir } from 'node:path'",
  'const require = __aliceRequire(import.meta.url)',
  'const __filename = __alicePath(import.meta.url)',
  'const __dirname = __aliceDir(__filename)',
].join(';')

await build({
  banner: { js: shims },
  bundle: true,
  entryPoints: ['src/main.ts'],
  format: 'esm',
  logLevel: 'warning',
  outfile: 'dist/main.mjs',
  platform: 'node',
  target: 'node22',
})
