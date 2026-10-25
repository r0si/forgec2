import { defineConfig } from 'vitest/config'
import { fileURLToPath } from 'node:url'

export default defineConfig({
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: [],
    include: ['src/**/*.test.{ts,tsx}'],
    exclude: ['node_modules', 'e2e', 'out'],
    coverage: {
      provider: 'v8',
      // Only our own source. Without this the report is dominated by the
      // vendored vis-network bundle under public/ (465 kB, sourcemap missing),
      // which dragged the statement total down to ~19% and made the number
      // meaningless as a regression signal.
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/**/*.test.{ts,tsx}',
        'src/**/*.d.ts',
        'src/**/__tests__/**',
        'src/test/**',
        // Locale dictionaries are data, not logic: ~4.8k string literals
        // between them that no unit test can meaningfully exercise. Their
        // correctness is guarded by `npm run check:i18n` (every t() key
        // resolves in both en and zh), which is the right shape of check for a
        // data file. Counting them here made the denominator ~7% of the total
        // and buried the real signal.
        'src/lib/i18n/en.ts',
        'src/lib/i18n/zh.ts',
      ],
      reporter: ['text-summary', 'json-summary'],
      reportsDirectory: 'coverage',
    },
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
      // Tests get the full dictionary synchronously (the production build
      // aliases this to an empty module and loads it as a separate chunk).
      '@i18n-sync': fileURLToPath(new URL('./src/lib/i18n/en.sync.ts', import.meta.url)),
    },
  },
})
