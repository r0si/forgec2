import { useEffect, useRef } from "react"

/**
 * Fires `callback` every `delay` ms, but pauses when the browser tab is hidden.
 * When the tab becomes visible again, it fires an immediate catch-up tick.
 */
export function useVisibleInterval(callback: () => void | PromiseLike<void>, delay: number) {
  const savedCallback = useRef(callback)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const runningRef = useRef(false)

  useEffect(() => {
    savedCallback.current = callback
  }, [callback])

  useEffect(() => {
    if (!Number.isFinite(delay) || delay <= 0 || delay > 2_147_483_647) return

    let disposed = false

    const tick = () => {
      // Polling callbacks commonly start an async request. Do not allow a
      // slow request to overlap with the next interval tick and create a
      // request pile-up when the backend is under pressure.
      if (disposed || document.hidden || runningRef.current) return
      runningRef.current = true
      try {
        const result = savedCallback.current()
        if (result && typeof (result as PromiseLike<void>).then === "function") {
          Promise.resolve(result).then(
            () => { runningRef.current = false },
            () => { runningRef.current = false },
          )
        } else {
          runningRef.current = false
        }
      } catch (error) {
        runningRef.current = false
        throw error
      }
    }

    const start = () => {
      if (timerRef.current) clearInterval(timerRef.current)
      timerRef.current = setInterval(tick, delay)
    }

    const handleVisibility = () => {
      if (document.hidden) {
        if (timerRef.current) clearInterval(timerRef.current)
        timerRef.current = null
      } else {
        tick()
        start()
      }
    }

    // A page can be mounted in a background tab. Do not create a timer until
    // it becomes visible; otherwise the first visibilitychange arrives only
    // after needless background wakeups have already occurred.
    if (!document.hidden) start()
    document.addEventListener("visibilitychange", handleVisibility)

    return () => {
      disposed = true
      if (timerRef.current) clearInterval(timerRef.current)
      timerRef.current = null
      // The pending callback still owns the single-flight reservation. Keep
      // it until settlement even when a new delay creates another timer.
      document.removeEventListener("visibilitychange", handleVisibility)
    }
  }, [delay])
}
