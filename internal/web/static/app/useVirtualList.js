// useVirtualList — hand-rolled windowing hook for the virtualized sidebar.
// PERF-K: renders only the rows in the viewport plus a small overscan,
// preserving keyboard navigation, ARIA list semantics, and scroll anchor
// across collapse/expand.
//
// Design goals:
//   - Handle mixed item types (40 px session rows + variable-height group headers)
//   - Binary search for first visible index (O(log n) on offsets array)
//   - ResizeObserver for dynamic viewport height measurement
//   - Overscan of 6 rows above/below the visible window so scrolling
//     does not expose blank space as the window slides
//   - ARIA-compatible: returns container props with role="list" and
//     aria-rowcount equal to the TOTAL item count (not the visible slice)
//
// Usage:
//   const { virtualItems, totalHeight, containerProps } = useVirtualList({
//     items,
//     estimateSize: (item) => item.type === 'group' ? 44 : 40,
//     overscan: 6,
//   })
//
// Return shape:
//   virtualItems: [{ index, item, offset, size }] — currently visible slice + overscan
//   totalHeight: number — for the absolutely-positioned scroll spacer
//   containerProps: { ref, onScroll, style, role, aria-rowcount }
//   scrollTo(index): imperative scroll-into-view helper for keyboard nav
//   setMeasuredSize(index, size): callers can record actual row heights
import { useState, useEffect, useRef, useMemo, useCallback } from 'preact/hooks'

export function useVirtualList({ items, estimateSize, overscan = 6 }) {
  const containerRef = useRef(null)
  const [scrollTop, setScrollTop] = useState(0)
  const [viewportHeight, setViewportHeight] = useState(0)
  // sizeCache: Map<index, measured height>. Falls back to estimateSize(item)
  // for items that have not yet been measured at their current index.
  const sizeCacheRef = useRef(new Map())
  const [cacheVersion, setCacheVersion] = useState(0)

  // offsets[i] is the y position of item i; offsets[items.length] is total height.
  const offsets = useMemo(() => {
    const arr = new Array(items.length + 1)
    arr[0] = 0
    for (let i = 0; i < items.length; i++) {
      const measured = sizeCacheRef.current.get(i)
      const size = measured != null ? measured : estimateSize(items[i])
      arr[i + 1] = arr[i] + size
    }
    return arr
  }, [items, estimateSize, cacheVersion])

  const totalHeight = offsets[items.length] || 0

  // Binary search: find the first index whose offset >= targetY.
  const findFirstVisible = useCallback((targetY) => {
    if (targetY <= 0) return 0
    let lo = 0
    let hi = offsets.length - 1
    while (lo < hi) {
      const mid = (lo + hi) >> 1
      if (offsets[mid] < targetY) {
        lo = mid + 1
      } else {
        hi = mid
      }
    }
    return Math.max(0, lo - 1)
  }, [offsets])

  const firstVisible = findFirstVisible(scrollTop)
  let lastVisible = firstVisible
  while (
    lastVisible < items.length &&
    offsets[lastVisible] < scrollTop + viewportHeight
  ) {
    lastVisible++
  }
  const start = Math.max(0, firstVisible - overscan)
  const end = Math.min(items.length, lastVisible + overscan)

  const virtualItems = []
  for (let i = start; i < end; i++) {
    virtualItems.push({
      index: i,
      item: items[i],
      offset: offsets[i],
      size: offsets[i + 1] - offsets[i],
    })
  }

  // Track viewport height via ResizeObserver so variable-height rows and
  // device orientation changes update the visible window correctly.
  useEffect(() => {
    const node = containerRef.current
    if (!node || typeof ResizeObserver !== 'function') return
    const ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        setViewportHeight(entry.contentRect.height)
      }
    })
    ro.observe(node)
    setViewportHeight(node.clientHeight)
    return () => ro.disconnect()
  }, [])

  const onScroll = useCallback((e) => {
    setScrollTop(e.currentTarget.scrollTop)
  }, [])

  // scrollTo implements scrollIntoView({ block: 'nearest' }) semantics:
  // if the target is already visible, do nothing; else scroll the minimum
  // amount to bring it into view. Used by keyboard ArrowUp/ArrowDown nav.
  const scrollTo = useCallback((index) => {
    const node = containerRef.current
    if (!node) return
    const target = offsets[index] || 0
    const itemEnd = offsets[index + 1] || target
    if (target < node.scrollTop) {
      node.scrollTop = target
    } else if (itemEnd > node.scrollTop + node.clientHeight) {
      node.scrollTop = itemEnd - node.clientHeight
    }
  }, [offsets])

  // Allow callers to record a measured size for a specific index.
  // Bumping cacheVersion forces the memoized offsets array to recompute.
  const setMeasuredSize = useCallback((index, size) => {
    const cached = sizeCacheRef.current.get(index)
    if (cached === size) return
    sizeCacheRef.current.set(index, size)
    setCacheVersion((v) => v + 1)
  }, [])

  const containerProps = {
    ref: containerRef,
    onScroll,
    style: { overflowY: 'auto', position: 'relative' },
    role: 'list',
    'aria-rowcount': items.length,
  }

  return {
    virtualItems,
    totalHeight,
    scrollTo,
    setMeasuredSize,
    containerProps,
  }
}
