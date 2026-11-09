/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { getFreshAuthHeaders } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { exportTrainingData } from '../../api'

vi.mock('@/lib/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/api')>()),
  getFreshAuthHeaders: vi.fn(),
}))

const encoder = new TextEncoder()
const rawLine = JSON.stringify({
  request_id: 'req-1',
  input: '中文 café',
  output: 'stored answer',
})
const success = JSON.stringify({ success: true, data: { count: 1 } })

function streamResponse(chunks: Uint8Array[]): Response {
  return new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        for (const chunk of chunks) controller.enqueue(chunk)
        controller.close()
      },
    })
  )
}

async function readBlob(blob: Blob): Promise<string> {
  if (typeof blob.text === 'function') return blob.text()
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.addEventListener('load', () => resolve(String(reader.result)))
    reader.addEventListener('error', () => reject(reader.error))
    reader.readAsText(blob)
  })
}

beforeEach(() => {
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  vi.mocked(getFreshAuthHeaders).mockReset().mockResolvedValue({
    Authorization: 'Bearer fresh-training-token',
  })
})

afterEach(() => {
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  vi.restoreAllMocks()
})

it.each(['raw', 'openai'] as const)(
  'exports %s with fresh authorization and filters, preserves split UTF-8, and strips the success trailer',
  async (format) => {
    const line =
      format === 'raw'
        ? rawLine
        : JSON.stringify({
            messages: [
              { role: 'user', content: '中文 café' },
              { role: 'assistant', content: 'stored answer' },
            ],
          })
    // Split inside a multibyte character and inside the trailer; no final newline.
    const bytes = encoder.encode(`${line}\n${success}`)
    const split = bytes.indexOf(0xe4) + 1
    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(
        streamResponse([
          bytes.slice(0, split),
          bytes.slice(split, split + 1),
          bytes.slice(split + 1, bytes.length - 8),
          bytes.slice(bytes.length - 8),
        ])
      )
    const controller = new AbortController()

    const result = await exportTrainingData(
      format,
      {
        model_name: 'gpt-4o + custom/中文',
        user_id: 42,
        start_timestamp: 1790000000,
        end_timestamp: 1790003600,
      },
      controller.signal
    )

    expect(getFreshAuthHeaders).toHaveBeenCalledTimes(1)
    expect(fetchSpy).toHaveBeenCalledTimes(1)
    const [url, init] = fetchSpy.mock.calls[0]
    const request = new URL(String(url), 'http://localhost')
    expect(request.pathname).toBe('/api/prompt-security/content/export')
    expect(Object.fromEntries(request.searchParams)).toEqual({
      format,
      model_name: 'gpt-4o + custom/中文',
      user_id: '42',
      start_timestamp: '1790000000',
      end_timestamp: '1790003600',
    })
    expect(init).toMatchObject({
      credentials: 'include',
      signal: controller.signal,
    })
    expect(new Headers(init?.headers).get('Authorization')).toBe(
      'Bearer fresh-training-token'
    )
    expect(result.count).toBe(1)
    expect(result.blob.type).toBe('application/x-ndjson;charset=utf-8')
    expect(await readBlob(result.blob)).toBe(`${line}\n`)
  }
)

it.each([
  ['missing trailer', `${rawLine}\n`],
  [
    'failed trailer',
    `${rawLine}\n${JSON.stringify({ success: false, message: 'Capture storage unavailable' })}\n`,
  ],
  ['corrupt JSON', `${rawLine}\n{broken json}\n${success}\n`],
  [
    'count mismatch',
    `${rawLine}\n${JSON.stringify({ success: true, data: { count: 2 } })}\n`,
  ],
  [
    'invalid count',
    `${rawLine}\n${JSON.stringify({ success: true, data: { count: -1 } })}\n`,
  ],
  ['data after trailer', `${rawLine}\n${success}\n${rawLine}\n`],
])(
  'rejects %s instead of returning a partial training Blob',
  async (_name, body) => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      streamResponse([encoder.encode(body)])
    )

    await expect(
      exportTrainingData('raw', {}, new AbortController().signal)
    ).rejects.toThrow()
  }
)

it('returns an empty Blob only for a successful zero-sample export', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(
    streamResponse([encoder.encode('{"success":true,"data":{"count":0}}\n')])
  )

  const result = await exportTrainingData(
    'openai',
    {},
    new AbortController().signal
  )

  expect(result.count).toBe(0)
  expect(result.blob.size).toBe(0)
  expect(await readBlob(result.blob)).toBe('')
})

it('rejects an HTTP refusal without treating its JSON error as training data', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(
    new Response(
      JSON.stringify({ success: false, message: 'Training export forbidden' }),
      { status: 403, headers: { 'Content-Type': 'application/json' } }
    )
  )

  await expect(
    exportTrainingData('raw', {}, new AbortController().signal)
  ).rejects.toThrow('Training export forbidden')
})

it('cancels an unfinished failed stream without returning its already received samples', async () => {
  const cancel = vi.fn()
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(
    new Response(
      new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(
            encoder.encode(
              `${rawLine}\n{"success":false,"message":"Export interrupted"}\n`
            )
          )
        },
        cancel,
      })
    )
  )

  await expect(
    exportTrainingData('raw', {}, new AbortController().signal)
  ).rejects.toThrow('Export interrupted')
  expect(cancel).toHaveBeenCalledTimes(1)
})

it('propagates abort during streaming without returning a partial Blob', async () => {
  const controller = new AbortController()
  let notifyReading!: () => void
  const reading = new Promise<void>((resolve) => {
    notifyReading = resolve
  })
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (_url, init) => {
    expect(init?.signal).toBe(controller.signal)
    let sent = false
    return new Response(
      new ReadableStream<Uint8Array>({
        start(stream) {
          init?.signal?.addEventListener(
            'abort',
            () => stream.error(controller.signal.reason),
            { once: true }
          )
        },
        pull(stream) {
          if (sent) {
            notifyReading()
            return
          }
          sent = true
          stream.enqueue(encoder.encode(`${rawLine}\n`))
        },
      })
    )
  })
  const pending = exportTrainingData('raw', {}, controller.signal)
  const rejected = expect(pending).rejects.toMatchObject({ name: 'AbortError' })

  await reading
  controller.abort()

  await rejected
})

it('rejects malformed UTF-8 rather than silently changing stored training content', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(
    streamResponse([
      encoder.encode('{"request_id":"req-1","input":"'),
      new Uint8Array([0xc3, 0x28]), // Invalid continuation byte inside otherwise valid JSON.
      encoder.encode(`"}\n${success}\n`),
    ])
  )

  await expect(
    exportTrainingData('raw', {}, new AbortController().signal)
  ).rejects.toThrow()
})
