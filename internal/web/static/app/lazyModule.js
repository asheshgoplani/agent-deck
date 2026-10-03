// lazyModule.js -- loads view code that first paint does not need. The
// promise is cached per key; a failed fetch clears it, so the next mount, or
// `needed` turning true again, retries.
import { useEffect, useState } from 'preact/hooks'

const lazyCache = {}

export function useLazyComponent(needed, loader, cacheKey, pick) {
  const [Comp, setComp] = useState(null)
  useEffect(() => {
    if (!needed || Comp) return
    let alive = true
    lazyCache[cacheKey] = lazyCache[cacheKey] || loader()
    lazyCache[cacheKey]
      .then(m => { if (alive) setComp(() => pick(m)) })
      .catch(() => { lazyCache[cacheKey] = null })
    return () => { alive = false }
  }, [needed, Comp])
  return Comp
}
