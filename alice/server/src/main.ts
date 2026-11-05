import { CreateApplication, TServiceParams } from '@digital-alchemy/core'
import { LIB_HASS } from '@digital-alchemy/hass'
import {
  Functions,
  FunctionStateServerStateResponse
} from '@v3rt3p/types/function-state-server'
import dotenv from 'dotenv'
import express from 'express'
import { createHash, timingSafeEqual } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import z from 'zod'

import { ActivityEvent, ActivityJournal, ActivityRecord } from './activity'
import { loadConfig } from './config'
import { aliasConflict, deviceVoiceNames, dictionaryReport, localVoicePhrases, resolveLightCommand } from './control'
import { buildDevices, DeviceEntry, snapshotEntity } from './devices'
import { executeFunction } from './execution'
import { createHomeAssistantApi, HomeAssistantApi } from './home-assistant'
import { getLogger } from './logger'
import { previewCommand } from './preview'
import { callReadTool, READ_TOOLS } from './read-tools'
import { isSpeakerAllowed, speakerDevices } from './speaker-access'

const logger = getLogger()

dotenv.config({
  path: '.env.local'
})
dotenv.config()

const CONFIG_PATH = process.env.CONFIG_PATH ?? 'config.yaml'
const PORT = Number.parseInt(process.env.PORT || '8080')

/**
 * Device list cache TTL: registry updates arrive over websocket instantly, so
 * this only protects against pathological request rates.
 */
const DEVICE_CACHE_MS = 1000

function collectFunctions (devices: Map<string, DeviceEntry>): Functions {
  const result: Functions = {}
  for (const device of devices.values()) {
    for (const function_ of device.functions) {
      result[function_.name] = {
        arguments: function_.arguments,
        description: function_.description,
        hasResponse: function_.hasResponse === true
      }
    }
  }
  return result
}

function collectState (homeAssistant: HomeAssistantApi,
  devices: Map<string, DeviceEntry>): FunctionStateServerStateResponse {
  const result: FunctionStateServerStateResponse = {}
  for (const device of devices.values()) {
    result[device.stateProvider.name] = {
      description: device.stateProvider.description,
      value: device.stateProvider.format(snapshotEntity(homeAssistant, device.entityId))
    }
  }
  return result
}

function findFunction (devices: Map<string, DeviceEntry>,
  name: string): DeviceEntry['functions'][number] | undefined {
  for (const device of devices.values()) {
    const function_ = device.functions.find(f => f.name === name)
    if (function_) {
      return function_
    }
  }
  return undefined
}

/**
 * home-functions exposes Home Assistant devices (selected by their HA labels) to the
 * processor, using the same function/state HTTP contract as function-state-server.
 */
function MainService ({ hass, lifecycle }: TServiceParams) {
  const homeAssistant: HomeAssistantApi = createHomeAssistantApi(hass, logger)

  /**
   * Rebuilt on demand from the live websocket-synced registry, so newly
   * labeled devices appear without a restart.
   */
  let devicesCache: undefined | { devices: Map<string, DeviceEntry>; updatedAt: number, }
  let devicesPromise: Promise<Map<string, DeviceEntry>> | undefined

  async function getDevices (shouldRefresh = false): Promise<Map<string, DeviceEntry>> {
    const now = Date.now()
    if (!shouldRefresh && devicesCache && now - devicesCache.updatedAt < DEVICE_CACHE_MS) {
      return devicesCache.devices
    }
    if (devicesPromise) {
      return devicesPromise
    }
    devicesPromise = (async () => {
      const devices = await buildDevices(homeAssistant, loadConfig(CONFIG_PATH))
      const previous = devicesCache?.devices
      for (const device of devices.values()) {
        if (!previous?.has(device.entityId)) {
          logger.info(`Exposed '${device.entityId}' as '${device.slug}' ` +
            `(${device.functions.length} functions)`)
        }
      }
      devicesCache = { devices, updatedAt: Date.now() }
      return devices
    })()
    try {
      return await devicesPromise
    } finally {
      devicesPromise = undefined
    }
  }

  lifecycle.onReady(async () => {
    try {
      await homeAssistant.loadServiceIndex()
    } catch (error: unknown) {
      logger.error(`Failed to load Home Assistant services: ${String(error)}`)
    }
    void getDevices()
    startHttpServer(homeAssistant, getDevices)
  })
}

function startHttpServer (homeAssistant: HomeAssistantApi,
  getDevices: (shouldRefresh?: boolean) => Promise<Map<string, DeviceEntry>>): void {
  const app = express()
  const activity = new ActivityJournal(process.env.ALICE_EVENTS_PATH)
  const accessToken = process.env.HOME_FUNCTIONS_TOKEN
  app.use((request, response, next) => {
    if (!accessToken || request.path === '/health') { next(); return }
    const expected = Buffer.from(`Bearer ${accessToken}`)
    const supplied = Buffer.from(request.get('authorization') ?? '')
    if (expected.length !== supplied.length || !timingSafeEqual(expected, supplied)) {
      response.sendStatus(401); return
    }
    next()
  })
  app.use(express.json())
  // Only the authenticated local manager can enumerate unexposed HA entities.
  app.get('/inventory', async (request, response) => {
    if (!['127.0.0.1', '::1', '::ffff:127.0.0.1'].includes(request.ip ?? '')) {
      response.sendStatus(403); return
    }
    try {
      await homeAssistant.loadServiceIndex()
      const config = loadConfig(CONFIG_PATH)
      const all = await buildDevices(homeAssistant, { ...config, entities: {}, expose_all: true })
      const registryItems = await homeAssistant.getRegistry()
      const registry = new Map(registryItems.map(item => [item.entityId, item]))
      const entities = []
      for (const item of all.values()) {
        if (!item.voicePhrases) {
          entities.push({
            controllable: item.functions.some(action => !action.hasResponse),
            entity_id: item.entityId,
            labels: registry.get(item.entityId)?.labels ?? [],
            name: item.name
          })
        }
      }
      response.json({ entities })
    } catch {
      response.status(503).json({ error: 'Не удалось обновить устройства Home Assistant. Попробуйте ещё раз.' })
    }
  })
  app.get('/events', async (_, response) => {
    try { response.json({ events: await activity.list() }) } catch { response.status(503).json({ error: 'Журнал временно недоступен.' }) }
  })
  app.post('/events', async (request, response) => {
    const parsed = ActivityEvent.safeParse(request.body)
    if (!parsed.success) { response.status(400).json({ error: 'Invalid activity event' }); return }
    if (request.get('x-alice-speaker') !== parsed.data.speaker_id) { response.sendStatus(403); return }
    try { await activity.append(parsed.data); response.json({ accepted: true }) } catch { response.status(503).json({ error: 'Activity storage unavailable' }) }
  })
  app.post('/preview', async (request, response) => {
    const previewBody = z.object({ area: z.string().max(100).optional(), text: z.string().min(1).max(2000) })
    const body = previewBody.safeParse(request.body)
    const speaker = request.get('x-alice-speaker')
    if (!speaker || !body.success) { response.status(400).json({ error: 'Invalid preview' }); return }
    response.json(previewCommand(body.data.text, body.data.area, speaker, await getDevices()))
  })
  app.get('/dictionary', async (request, response) => {
    const speaker = request.get('x-alice-speaker')
    const devices = speakerDevices(await getDevices(), speaker)
    let events: ActivityRecord[] = []
    try { events = await activity.list() } catch { /* the page still shows the table */ }
    // Aliases live in HA and serve every speaker, so misses are collected from all of them.
    response.json(dictionaryReport(devices, events))
  })

  // Written only by an explicit button in settings; the answer is always 200 so
  // the settings page can show the reason.
  app.post('/dictionary/alias', async (request, response) => {
    const aliasBody = z.object({
      alias: z.string().trim().min(2).max(60), entity_id: z.string().max(200), remove: z.boolean().optional()
    }).strict()
    const body = aliasBody.safeParse(request.body)
    const devices = await getDevices()
    const device = body.success ? devices.get(body.data.entity_id) : undefined
    if (!device || !body.success || device.voicePhrases) {
      response.json({ error: 'Устройство недоступно Алисе.', saved: false })
      return
    }
    if (body.data.remove) {
      const result = await homeAssistant.removeEntityAlias(device.entityId, body.data.alias)
      response.json(result === 'removed' ? { saved: true } : { error: 'Это имя задано не в Home Assistant.', saved: false })
      return
    }
    const owner = aliasConflict(body.data.alias, device.entityId, devices)
    if (owner) {
      response.json({ error: `Так уже называется «${owner}».`, saved: false })
      return
    }
    await homeAssistant.addEntityAlias(device.entityId, body.data.alias)
    response.json({ saved: true })
  })

  // Speaker upgrades keep their own config, so routes reach them with this table.
  const publishedRouting = async (): Promise<unknown> => {
    if (!process.env.ALICE_ROUTING_PATH) return undefined
    try {
      const config = JSON.parse(await readFile(process.env.ALICE_ROUTING_PATH, 'utf8')) as { routing?: unknown }
      return config.routing
    } catch { return undefined }
  }
  app.get('/voice-phrases', async (request, response) => {
    const commands = []
    const devices = await getDevices()
    for (const device of devices.values()) {
      if (device.voicePhrases) commands.push({ enabled: isSpeakerAllowed(device.entityId, request.get('x-alice-speaker')), phrases: device.voicePhrases, target: device.entityId })
    }
    // Not a real script: the speaker keeps only these targets, and resolves every
    // match through /resolve, so this entry just carries the local dictionary.
    const allowed = speakerDevices(devices, request.get('x-alice-speaker'))
    const local = localVoicePhrases(allowed)
    if (local.length > 0) commands.push({ enabled: true, phrases: local, target: 'script.alice_local_dictionary' })
    const revision = createHash('sha256').update(JSON.stringify(commands)).digest('hex').slice(0, 16)
    response.json({ commands, device_names: deviceVoiceNames(allowed), revision, routing: await publishedRouting() })
  })

  /**
   * Metadata-dependent request bodies. Metadata is accepted (and validated) but
   * ignored: the exposed Home Assistant devices do not depend on processor
   * session metadata, so PUT returns the same data as GET.
   */
  const metadataRequest = z.object({
    metadata: z.record(z.string(), z.any()).default({}),
    sessionId: z.string().optional()
  })

  const argumentValues = z.record(z.string(), z.union([z.string(), z.number()]))

  const callFunctionRequest = z.object({
    arguments: argumentValues.nullish().transform(value => value ?? {}),
    metadata: z.record(z.string(), z.any()).default({}),
    name: z.string(),
    sessionId: z.string().optional()
  })

  // Stateless, authenticated MCP: read-only tools only; never exposes service calls.
  app.post('/mcp', async (request, response) => {
    const mcpParameters = z.object({
      arguments: z.record(z.string(), z.unknown()).optional(), name: z.string().optional()
    })
    const parsed = z.object({
      id: z.union([z.string(), z.number(), z.null()]).optional(),
      jsonrpc: z.literal('2.0'),
      method: z.string(),
      params: mcpParameters.optional()
    }).safeParse(request.body)
    if (!parsed.success) {
      response.status(400).json({ error: 'invalid_jsonrpc' }); return
    }
    const message = parsed.data
    if (message.id === undefined) { response.sendStatus(202); return }
    const reply = (result: unknown): void => { response.json({ id: message.id, jsonrpc: '2.0', result }) }
    if (message.method === 'initialize') {
      reply({
        capabilities: { tools: { listChanged: false } },
        protocolVersion: '2025-03-26',
        serverInfo: { name: 'alice-home-read', version: '1.0.0' }
      }); return
    }
    if (message.method === 'ping') { reply({}); return }
    if (message.method === 'tools/list') { reply({ tools: READ_TOOLS }); return }
    if (message.method !== 'tools/call' || !message.params?.name || READ_TOOLS.every(t => t.name !== message.params?.name)) {
      response.json({ error: { code: -32_601, message: 'Unknown method or tool' }, id: message.id, jsonrpc: '2.0' }); return
    }
    try {
      const arguments_ = message.params.arguments ?? {}
      const result = await callReadTool(message.params.name, arguments_, homeAssistant, await getDevices())
      reply({ content: [{ text: JSON.stringify(result), type: 'text' }], isError: false })
    } catch {
      reply({ content: [{ text: 'Home Assistant read failed. Check the exposed entity and period; do not invent a result.', type: 'text' }], isError: true })
    }
  })

  // metadata-independent state
  app.post('/resolve', async (request, response) => {
    const body = z.object({
      area: z.string().max(100).optional(), text: z.string().max(2000), voice_only: z.boolean().optional()
    }).safeParse(request.body)
    if (!body.success) { response.status(400).json({ error: 'Invalid command' }); return }
    const result = resolveLightCommand(body.data.text, body.data.area, speakerDevices(await getDevices(), request.get('x-alice-speaker')), body.data.voice_only)
    response.json({ ...result, clarify: result.calls.length === 0 && (result.answer?.endsWith('?') ?? false), voice_match: body.data.voice_only === true && result.matched })
  })

  app.get('/state', async (_, response) => {
    response.status(200).json(collectState(homeAssistant, await getDevices()))
  })

  // metadata-dependent state
  app.put('/state', async (request, response) => {
    metadataRequest.parse(request.body ?? {})
    response.status(200).json(collectState(homeAssistant, await getDevices()))
  })

  // metadata-independent functions
  app.get('/functions', async (request, response) => {
    const devices = speakerDevices(await getDevices(), request.get('x-alice-speaker'))
    response.status(200).json(collectFunctions(devices))
  })

  // metadata-dependent functions
  app.put('/functions', async (request, response) => {
    metadataRequest.parse(request.body ?? {})
    const devices = speakerDevices(await getDevices(), request.get('x-alice-speaker'))
    response.status(200).json(collectFunctions(devices))
  })

  // invoke function
  app.post('/functions', (request, response) => {
    const parsed = callFunctionRequest.safeParse(request.body ?? {})
    if (!parsed.success) { response.status(400).json({ error: 'Invalid function call' }); return }
    const body = parsed.data
    const started = Date.now()
    void (async () => {
      try {
        const devices = speakerDevices(await getDevices(true), request.get('x-alice-speaker'))
        const function_ = findFunction(devices, body.name)
        let device: DeviceEntry | undefined
        for (const entry of devices.values()) {
          if (entry.functions.some(f => f.name === body.name)) { device = entry; break }
        }
        if (!function_ || !device) { response.status(404).json({ error: 'No such function exists' }); return }
        const result = await executeFunction(homeAssistant, device, function_, body.arguments)
        logger.info(`Function '${body.name}': ${result.execution?.status ?? 'read'}; ${Date.now() - started}ms`)
        response.status(200).json(result)
      } catch (error: unknown) {
        logger.warn(`Failed to call '${body.name}': ${String(error)}`)
        response.status(500).json({ error: String(error) })
      }
    })()
  })

  app.get('/health', (_, response) => {
    response.status(200).json({ status: 'ok' })
  })

  app.listen(PORT, error => {
    if (error) {
      logger.fatal(`Failed to start on :${PORT}: ${error}`)
      return
    }
    logger.info(`Serving functions and states on :${PORT}`)
  })
}

export const HOME_APPLICATION = CreateApplication({
  libraries: [LIB_HASS],
  name: 'home_functions',
  services: { main: MainService }
})

declare module '@digital-alchemy/core' {
  export interface LoadedModules {
    home_functions: typeof HOME_APPLICATION;
  }
}

void HOME_APPLICATION.bootstrap().catch((error: unknown) => {
  logger.fatal(`Failed to bootstrap: ${String(error)}`)
  process.exit(1)
})
