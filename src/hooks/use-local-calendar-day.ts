import { useEffect, useState } from 'react'

function calendarSnapshot() {
  const date = new Date()
  return {
    date,
    key: `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}-${date.getTimezoneOffset()}`,
  }
}

/** Refresh local calendar dates at midnight and when returning to the app. */
export function useLocalCalendarDay(): Date {
  const [day, setDay] = useState(calendarSnapshot)

  useEffect(() => {
    let timer: number | undefined

    const refresh = () => {
      const snapshot = calendarSnapshot()
      setDay((previous) => previous.key === snapshot.key ? previous : snapshot)

      // Local midnight may be 23 or 25 hours away across a DST transition.
      const nextMidnight = new Date(snapshot.date)
      nextMidnight.setHours(24, 0, 0, 0)
      window.clearTimeout(timer)
      timer = window.setTimeout(refresh, nextMidnight.getTime() - snapshot.date.getTime())
    }

    const onVisibilityChange = () => {
      if (document.visibilityState === 'visible') refresh()
    }

    refresh()
    window.addEventListener('focus', refresh)
    document.addEventListener('visibilitychange', onVisibilityChange)
    return () => {
      window.clearTimeout(timer)
      window.removeEventListener('focus', refresh)
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }, [])

  return day.date
}
