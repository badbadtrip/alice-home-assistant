import fs from 'node:fs'
import { parse } from 'yaml'
import z from 'zod'

const renameType = z.union([z.string(), z.array(z.string())])
const modesType = z.record(z.string(), renameType)

const entityConfigType = z.object({
  area: z.string().optional(),
  description: z.string().optional(),
  lighting: z.boolean().optional(),
  modes: z.record(z.string(), modesType).default({}),
  names: z.array(z.string()).default([]),
  slug: z.string().optional()
})

const expectedStateType = z.object({ entity_id: z.string(), state: z.string() })
const voiceCommandType = z.object({
  automation: z.string().regex(/^(automation|script)\.[a-z0-9_]+$/u),
  description: z.string().min(1),
  phrases: z.array(z.string().min(1)).min(1),
  verify: z.array(expectedStateType).default([])
})
const voiceCommandKeyType = z.string().regex(/^[a-z][a-z0-9_]*$/u)
const configType = z.object({
  entities: z.record(z.string(), entityConfigType).default({}),
  labels: z.array(z.string()).default([]),
  voice_command_label: z.string().min(1).optional(),
  voice_commands: z.record(voiceCommandKeyType, voiceCommandType).optional()
})

export type EntityConfig = z.infer<typeof entityConfigType>
export type HomeFunctionsConfig = z.infer<typeof configType>

export function loadConfig (path: string): HomeFunctionsConfig {
  const raw = fs.readFileSync(path).toString('utf8')
  return configType.parse(raw.trim() === '' ? {} : parse(raw))
}
