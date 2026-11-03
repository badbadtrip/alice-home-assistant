import { setTimeout as delay } from 'node:timers/promises'

import { JsonSchema } from './arguments'
import { DeviceEntry } from './devices'
import { DeviceFunction } from './domains/types'
import { HomeAssistantApi } from './home-assistant'

export interface ExecutionResult {
  entity_id: string;
  name: string;
  status: 'accepted' | 'already' | 'confirmed' | 'unavailable';
  target_state?: string | undefined;
}

export async function executeFunction (api: HomeAssistantApi, device: DeviceEntry,
  function_: DeviceFunction, values: Record<string, number | string>, timeoutMs = 2000):
Promise<{ execution?: ExecutionResult; response?: string | undefined; }> {
  validateArguments(function_.arguments, values)
  const result = (status: ExecutionResult['status']): { execution: ExecutionResult } => ({
    execution: {
      entity_id: device.entityId,
      name: device.name ?? device.entityId,
      status,
      target_state: function_.expectedState?.(values)
    }
  })
  const before = api.getEntityState(device.entityId)
  if (!function_.hasResponse && (!before || ['unavailable', 'unknown'].includes(before.state))) return result('unavailable')
  const target = function_.expectedState?.(values)
  const defaultExpected = target === undefined ? [] : [{ entityId: device.entityId, state: target }]
  const expected = function_.expectedStates ?? defaultExpected
  const isTargetState = (): boolean => expected.every(item => api.getEntityState(item.entityId)?.state === item.state)
  if (expected.length > 0 && isTargetState()) return result('already')
  // Never retry a service call: timeouts can happen after the action was accepted.
  const response = await function_.invoke(values)
  if (function_.hasResponse) return { response }
  if (expected.length === 0) return result('accepted')
  const deadline = Date.now() + timeoutMs
  do {
    if (isTargetState()) return result('confirmed')
    if (Date.now() >= deadline) return result('accepted')
    const remaining = deadline - Date.now()
    await delay(Math.min(100, Math.max(1, remaining)))
  } while (Date.now() <= deadline)
  return result('accepted')
}

// The generated catalog uses object arguments and scalar schemas only.
export function validateArguments (schema: JsonSchema, values: Record<string, unknown>): void {
  const properties = (schema.properties ?? {}) as Record<string, JsonSchema>
  if (Object.keys(values).some(key => !Object.hasOwn(properties, key))) throw new Error('Unknown argument')
  const required = (schema.required ?? []) as string[]
  if (required.some(key => !Object.hasOwn(values, key))) throw new Error('Missing argument')
  for (const [key, value] of Object.entries(values)) {
    if (!isValidScalar(properties[key] ?? {}, value)) throw new Error(`Invalid argument: ${key}`)
  }
}

function isValidScalar (schema: JsonSchema, value: unknown): boolean {
  if (Object.hasOwn(schema, 'const') && value !== schema.const) return false
  if (Array.isArray(schema.enum) && !schema.enum.includes(value)) return false
  const variants = schema.anyOf
  if (Array.isArray(variants) && variants.every(s => !isValidScalar(s as JsonSchema, value))) return false
  if (schema.type === 'number' && (typeof value !== 'number' || !Number.isFinite(value))) return false
  if (typeof value !== 'string' && schema.type === 'string') return false
  if (typeof value === 'string') return true
  if (typeof value !== 'number' || !Number.isFinite(value)) return false
  const isBelowMinimum = typeof schema.minimum === 'number' && value < schema.minimum
  const isAboveMaximum = typeof schema.maximum === 'number' && value > schema.maximum
  return !isBelowMinimum && !isAboveMaximum
}
