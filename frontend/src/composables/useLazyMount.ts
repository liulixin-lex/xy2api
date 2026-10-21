import { defineComponent, h, ref, shallowRef, watch, type AsyncComponentLoader, type Component, type Ref, type WatchSource } from 'vue'
import { isChunkLoadError, recoverFromChunkLoadError } from '@/utils/chunkLoadError'

/**
 * 首次变为 true 后一直保持 true。
 * 用于大弹窗按需挂载：页面打开时不执行弹窗的 setup，首次打开才挂载；
 * 之后保留实例，关闭时照常播放离场动画，再次打开与原先行为一致。
 */
export function useLazyMount(source: WatchSource<unknown>): Readonly<Ref<boolean>> {
  const mounted = ref(false)
  watch(
    source,
    (value) => {
      if (value) mounted.value = true
    },
    { immediate: true, flush: 'sync' }
  )
  return mounted
}

/** 空闲时预取异步组件的 chunk，首次打开时无需等待网络。 */
export function prefetchWhenIdle(loaders: Array<() => Promise<unknown>>, timeout = 2000): () => void {
  if (typeof window === 'undefined') return () => {}
  let cancelled = false
  const run = () => {
    if (cancelled) return
    for (const load of loaders) load().catch(() => {})
  }
  if (typeof window.requestIdleCallback === 'function') {
    const id = window.requestIdleCallback(run, { timeout })
    return () => {
      cancelled = true
      window.cancelIdleCallback(id)
    }
  }
  const id = window.setTimeout(run, timeout)
  return () => {
    cancelled = true
    window.clearTimeout(id)
  }
}

/**
 * 按需加载、首次打开才挂载的组件。chunk 加载失败（新版本部署后旧文件已删除）时整页刷新拿到新版本，
 * 而不是让弹窗静默打不开。
 *
 * 内层组件第一次渲染时，打开状态（默认 `show`）一律传 false，挂载完成后再传真实值。
 * 这些弹窗原本在页面加载时就以关闭状态挂载，靠监听 `show` 从 false 变为 true 去加载数据
 * （测试连接的模型列表、临时不可调度原因等）。如果组件一挂载 `show` 就是 true，监听不会触发，
 * 第一次打开就是空的，关掉再开才正常。先关后开让组件看到的仍是一次正常的“打开”。
 */
export function defineLazyComponent(loader: AsyncComponentLoader, openProp = 'show') {
  let resolved: Component | null = null
  let pending: Promise<Component> | null = null
  const load = () => {
    pending ??= loader().then(
      (mod) => {
        const component = (mod as { default?: Component }).default ?? (mod as Component)
        resolved = component
        return component
      },
      (error: unknown) => {
        pending = null
        throw error
      }
    )
    return pending
  }

  return defineComponent({
    name: 'LazyComponent',
    inheritAttrs: false,
    setup(_, { attrs, slots }) {
      const component = shallowRef<Component | null>(resolved)
      const settled = ref(false)
      if (!resolved) {
        load().then(
          (loaded) => {
            component.value = loaded
          },
          (error: unknown) => {
            if (isChunkLoadError(error)) recoverFromChunkLoadError()
            else console.error('Failed to load lazy component:', error)
          }
        )
      }
      const markSettled = () => {
        settled.value = true
      }
      return () => {
        if (!component.value) return null
        const props: Record<string, unknown> = { ...attrs }
        if (!settled.value) {
          if (openProp in props) props[openProp] = false
          props.onVnodeMounted = markSettled
        }
        return h(component.value, props, slots)
      }
    }
  })
}
