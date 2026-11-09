import { defineConfig } from 'vitest/config'
import { resolve } from 'path'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
      'vue-i18n': 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js'
    }
  },
  server: {
    fs: { allow: [__dirname, resolve(__dirname, '../docs/legal')] }
  },
  test: {
    // Keep paired idle-callback polyfills backed by fake setTimeout/clearTimeout.
    fakeTimers: {
      toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date',
        'setImmediate', 'clearImmediate', 'hrtime', 'performance',
        'requestAnimationFrame', 'cancelAnimationFrame']
    },
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/__tests__/setup.ts'],
    include: ['src/**/*.{test,spec}.{js,ts,jsx,tsx}'],
    exclude: ['node_modules', 'dist'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json', 'html'],
      include: ['src/**/*.{js,ts,vue}'],
      exclude: [
        'node_modules',
        'src/**/*.d.ts',
        'src/**/*.spec.ts',
        'src/**/*.test.ts',
        'src/main.ts'
      ],
      thresholds: {
        // Ratchet from the measured baseline; 80% remains the long-term target.
        statements: 69,
        branches: 59,
        functions: 59,
        lines: 71
      }
    }
  }
})
