import { HomeFunctionsConfig } from './config'
import { normalizeVoicePhrase } from './control'
import { DeviceEntry } from './devices'
import { HomeAssistantApi, RegistryEntry } from './home-assistant'
import { slugify } from './translit'

// Only explicitly configured automations are exposed. No arbitrary service or
// event forwarding and no global interpretation of negative commands.
export function addVoiceCommands (api: HomeAssistantApi, devices: Map<string, DeviceEntry>,
  config: HomeFunctionsConfig, registry: RegistryEntry[] = []): void {
  const phrases = new Set<string>()
  const names = new Set<string>()
  for (const device of devices.values()) {
    for (const function_ of device.functions) names.add(function_.name)
  }
  const commands = Object.entries(registryCommands(api, config, registry))
  for (const [key, command] of commands) {
    for (const phrase of command.phrases) {
      const normalized = normalizeVoicePhrase(phrase)
      if (!normalized || phrases.has(normalized)) throw new Error('Duplicate or empty voice phrase')
      phrases.add(normalized)
    }
    const slug = `voice_${key}`
    const name = `run_${slug}`
    if (names.has(name)) throw new Error('Duplicate voice function')
    names.add(name)
    if (devices.has(command.automation)) throw new Error('Duplicate voice automation')
    devices.set(command.automation, {
      entityId: command.automation,
      functions: [{
        arguments: { additionalProperties: false, properties: {}, type: 'object' },
        description: command.description,
        expectedStates: command.verify.map(item => ({ entityId: item.entity_id, state: item.state })),
        invoke: async (): Promise<undefined> => {
          if (command.automation.startsWith('automation.')) {
            if (api.getEntityState(command.automation)?.state !== 'on') throw new Error('Voice automation is disabled')
            await api.callService('automation', 'trigger', { entity_id: command.automation, skip_condition: false })
          } else {
            await api.callService('script', 'turn_on', { entity_id: command.automation })
          }
        },
        name
      }],
      name: command.description,
      slug,
      stateProvider: {
        description: `Voice command: ${command.description}; phrases: ${command.phrases.join('; ')}`,
        format: state => JSON.stringify({
          automation_enabled: command.automation.startsWith('automation.') ? state?.state === 'on' : undefined,
          checks: command.verify.map(item => ({
            current: api.getEntityState(item.entity_id)?.state ?? 'unavailable',
            entity_id: item.entity_id,
            target: item.state
          }))
        }),
        name: `current_${slug}_state`
      },
      voicePhrases: command.phrases
    })
  }
}

function registryCommands (api: HomeAssistantApi, config: HomeFunctionsConfig, registry: RegistryEntry[]):
NonNullable<HomeFunctionsConfig['voice_commands']> {
  if (!config.voice_command_label) return config.voice_commands ?? {}
  const selected = registry.filter(item => item.labels.includes(config.voice_command_label as string) &&
    /^(automation|script)\./u.test(item.entityId))
  const owners = new Map<string, Set<string>>()
  for (const item of selected) {
    const aliases = item.aliases ?? []
    for (const alias of aliases) {
      const phrase = normalizeVoicePhrase(alias)
      const entities = owners.get(phrase) ?? new Set<string>()
      entities.add(item.entityId); owners.set(phrase, entities)
    }
  }
  const result: NonNullable<HomeFunctionsConfig['voice_commands']> = {}
  for (const item of selected) {
    const unique = new Map((item.aliases ?? []).map(alias => [normalizeVoicePhrase(alias), alias]))
    const phrases: string[] = []
    for (const [phrase, alias] of unique) {
      if (phrase.length > 0 && phrase.length <= 1000 && owners.get(phrase)?.size === 1) phrases.push(alias)
    }
    if (phrases.length === 0) continue
    const legacy = Object.values(config.voice_commands ?? {}).find(c => c.automation === item.entityId)
    result[slugify(item.entityId)] = {
      automation: item.entityId,
      description: (item.name || api.getEntityState(item.entityId)?.name || legacy?.description) || item.entityId,
      phrases,
      verify: legacy?.verify ?? []
    }
  }
  return result
}
