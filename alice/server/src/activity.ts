import { mkdir, readFile, rename, writeFile } from 'node:fs/promises'
import path from 'node:path'
import z from 'zod'

const ActivityCall = z.object({
  name: z.string().max(200), status: z.string().max(40), target: z.string().max(200)
})

export const ActivityEvent = z.object({
  at: z.iso.datetime(),
  calls: z.array(ActivityCall).max(4).default([]),
  duration_ms: z.number().int().min(0).max(600_000),
  error: z.string().max(200).optional(),
  id: z.string().regex(/^[a-f0-9-]{16,64}$/u),
  llm_ms: z.number().int().min(0).max(600_000).optional(),
  model: z.string().max(64).optional(),
  route: z.string().max(32),
  source: z.string().max(64),
  speaker_id: z.string().regex(/^[a-z0-9][a-z0-9_-]{0,63}$/u),
  status: z.enum(['handled', 'forwarded', 'answered', 'accepted', 'confirmed', 'already', 'clarification', 'unavailable', 'error', 'cancelled']),
  text: z.string().min(1).max(2000)
}).strict()

export type ActivityRecord = z.infer<typeof ActivityEvent>

// Only structured command metadata: never audio, credentials or full LLM replies.
export class ActivityJournal {
  private entries: ActivityRecord[] = []
  private hasLoadFailure = false
  private pending = Promise.resolve()
  private ready: Promise<void>

  constructor (private path?: string, private limit = 200) {
    this.ready = (async () => {
      try { await this.load() } catch { this.hasLoadFailure = true }
    })()
  }

  async append (input: unknown): Promise<void> {
    const event = ActivityEvent.parse(input)
    const previous = this.pending
    const operation = (async () => {
      await previous
      await this.ready
      if (this.hasLoadFailure) throw new Error('Activity storage invalid')
      const entries = this.recent().filter(item => item.id !== event.id)
      entries.push(event)
      const next = entries.slice(-this.limit)
      if (this.path) {
        await mkdir(path.dirname(this.path), { mode: 0o700, recursive: true })
        const temporary = this.path + '.tmp'
        await writeFile(temporary, JSON.stringify(next), { mode: 0o600 })
        await rename(temporary, this.path)
      }
      this.entries = next
    })()
    this.pending = (async () => { try { await operation } catch { /* next append can retry */ } })()
    await operation
  }

  async list (): Promise<ActivityRecord[]> {
    await this.ready
    await this.pending
    if (this.hasLoadFailure) throw new Error('Activity storage invalid')
    const entries = structuredClone(this.recent())
    const newestFirst: ActivityRecord[] = []
    for (let index = entries.length - 1; index >= 0; index--) {
      const entry = entries[index]
      if (entry) newestFirst.push(entry)
    }
    return newestFirst
  }

  private async load (): Promise<void> {
    if (!this.path) return
    try {
      const data: unknown = JSON.parse(await readFile(this.path, 'utf8'))
      this.entries = z.array(ActivityEvent).max(this.limit).parse(data)
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
    }
  }

  private recent (): ActivityRecord[] {
    const cutoff = Date.now() - 7 * 24 * 60 * 60 * 1000
    return this.entries.filter(item => Date.parse(item.at) >= cutoff)
  }
}
