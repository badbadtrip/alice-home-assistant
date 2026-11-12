import type { Express, Request, Response } from 'express'

import { request as httpRequest } from 'node:http'

// The Sendspin service listens on loopback only; this server already checked the
// speaker's token, so it only forwards the speaker id it was given.
const PORT = Number.parseInt(process.env.ALICE_SENDSPIN_PORT ?? '18095')

export function addSendspinRoutes (app: Express): void {
  app.get('/sendspin/stream', (request, response) => {
    request.socket.setTimeout(0)
    forward(request, response, 'GET', '/stream')
  })
  app.get('/sendspin/state', (request, response) => { forward(request, response, 'GET', '/state') })
  app.post('/sendspin/control', (request, response) => {
    forward(request, response, 'POST', '/control', JSON.stringify(request.body ?? {}))
  })
  app.post('/sendspin/play', (request, response) => {
    forward(request, response, 'POST', '/play', JSON.stringify(request.body ?? {}))
  })
}

function forward (request: Request, response: Response, method: 'GET' | 'POST', path: string, body?: string): void {
  const speaker = request.get('x-alice-speaker') ?? ''
  if (speaker.length > 64) { response.status(400).json({ error: 'Invalid speaker' }); return }
  const headers: Record<string, string> = { 'x-alice-speaker': speaker }
  if (body !== undefined) {
    headers['content-type'] = 'application/json'
    headers['content-length'] = String(Buffer.byteLength(body))
  }
  const upstream = httpRequest({ headers, host: '127.0.0.1', method, path, port: PORT }, reply => {
    response.status(reply.statusCode ?? 502)
    const type = reply.headers['content-type']
    if (type) response.setHeader('content-type', type)
    response.setHeader('cache-control', 'no-store')
    response.flushHeaders()
    reply.pipe(response)
  })
  upstream.on('error', () => {
    if (response.headersSent) response.end()
    else response.status(503).json({ error: 'Music Assistant service is not running' })
  })
  // A speaker that drops its request must free the stream for its next one.
  response.on('close', () => { upstream.destroy() })
  if (body === undefined) upstream.end()
  else upstream.end(body)
}
