// useDebounced — tiny Preact hook that returns `value` updated `delay` ms
// after the raw input stops changing. PERF-F: used by SessionList.js to
// debounce the search filter so the fuzzyMatch closure does not rerun on
// every keystroke.
//
// Usage:
//   const rawSearch = searchQuerySignal.value
//   const debouncedSearch = useDebounced(rawSearch, 250)
//   const filtered = items.filter(i => matchesSearch(i, debouncedSearch))
//
// The hook intentionally returns the raw value on the first render so the
// filter has something to match against before the 250ms timer fires.
import { useState, useEffect } from 'preact/hooks'

export function useDebounced(value, delay) {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const handle = setTimeout(() => setDebounced(value), delay)
    return () => clearTimeout(handle)
  }, [value, delay])
  return debounced
}
