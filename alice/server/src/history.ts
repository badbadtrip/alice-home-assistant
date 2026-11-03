import { EntityHistoryEntry } from './home-assistant'

/**
Aggregate state intervals, clipping HA's initial pre-window state to the window.
*/
export function summarizeHistory (entries: EntityHistoryEntry[], start: Date, end: Date,
  isNumeric: boolean): Record<string, unknown> {
  const rows = entries.map(entry => ({ state: entry.state, time: Date.parse(entry.lastChanged) }))
    .filter(entry => Number.isFinite(entry.time) && entry.time <= end.getTime())
    // Sorting a newly allocated array; ES2022 target has no toSorted type.
    // eslint-disable-next-line unicorn/no-array-sort
    .sort((a, b) => a.time - b.time)
  let knownMs = 0; let activeMs = 0; let weighted = 0; let activations = 0
  let minimum = Infinity; let maximum = -Infinity
  const events: Array<{ state: string; time: string; }> = []
  let previous: string | undefined
  for (let index = 0; index < rows.length; index++) {
    const row = rows[index]
    if (!row) continue
    const from = Math.max(start.getTime(), row.time)
    const to = Math.min(end.getTime(), rows[index + 1]?.time ?? end.getTime())
    const duration = Math.max(0, to - from)
    const isValid = !['', 'unavailable', 'unknown'].includes(row.state)
    if (isNumeric) {
      const value = Number(row.state)
      if (isValid && Number.isFinite(value) && duration > 0) {
        minimum = Math.min(minimum, value); maximum = Math.max(maximum, value)
        knownMs += duration; weighted += value * duration
      }
    } else {
      if (isValid) knownMs += duration
      const isActive = ['home', 'on', 'open', 'opening', 'playing'].includes(row.state)
      if (isValid && isActive) activeMs += duration
      if (row.time >= start.getTime() && row.state !== previous) {
        if (isActive && previous !== undefined && !['home', 'on', 'open', 'opening', 'playing', 'unavailable', 'unknown'].includes(previous)) activations++
        events.push({ state: row.state, time: new Date(row.time).toISOString() })
      }
    }
    previous = row.state
  }
  const common = {
    available: knownMs > 0,
    coverage_percent: round(100 * knownMs / (end.getTime() - start.getTime())),
    end: end.toISOString(),
    note: 'Only recorded intervals are known; missing data is not zero. Initial active state is not counted as a new activation.',
    start: start.toISOString()
  }
  if (isNumeric) {
    return {
      ...common,
      maximum: knownMs > 0 ? maximum : null,
      minimum: knownMs > 0 ? minimum : null,
      time_weighted_mean: knownMs ? round(weighted / knownMs) : null
    }
  }
  return {
    ...common,
    activations,
    active_hours: round(activeMs / 3_600_000),
    events: events.slice(-30),
    events_truncated: events.length > 30
  }
}

function round (value: number): number {
  return Math.round(value * 100) / 100
}
