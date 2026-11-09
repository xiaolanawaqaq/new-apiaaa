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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { AxiosAdapter } from 'axios'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import type { TrainingSample, TrainingSamplesResponse } from '../../types'
import { TrainingDataView } from '../dialogs/training-data-view'

const sample: TrainingSample = {
  request_id: 'request-full-content',
  created_at: 1790000000,
  username: 'alice',
  model_name: 'gpt-4o',
  request_path: '/v1/chat/completions',
  input_preview: 'i'.repeat(200),
  output_preview: 'o'.repeat(200),
  has_response: true,
  input_bytes: 240,
  output_bytes: 240,
}
const originalAdapter = api.defaults.adapter
let queryClient: QueryClient
let respond: (url: URL) => unknown | Promise<unknown>
const requests: URL[] = []
const adapter = vi.fn<AxiosAdapter>(async (config) => {
  const url = new URL(config.url ?? '', 'http://localhost')
  requests.push(url)
  return {
    data: await respond(url),
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }
})

function samples(
  items: TrainingSample[] = [],
  total = items.length
): TrainingSamplesResponse {
  return { success: true, data: { page: 1, page_size: 20, total, items } }
}

function renderBrowser(role: number = ROLE.ADMIN) {
  useAuthStore.setState((state) => ({
    auth: {
      ...state.auth,
      user: { id: 7, username: 'admin', role },
      accessToken: 'test-access-token',
      accessExpiresAt: Math.floor(Date.now() / 1000) + 3600,
      bootstrapState: 'complete',
    },
  }))
  return render(
    <QueryClientProvider client={queryClient}>
      <TrainingDataView />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  localStorage.clear()
  useAuthStore.getState().auth.reset()
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  requests.length = 0
  respond = () => samples()
  api.defaults.adapter = adapter
})

afterEach(() => {
  cleanup()
  queryClient.clear()
  api.defaults.adapter = originalAdapter
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  vi.restoreAllMocks()
})

it('renders no training UI and makes no request for an ordinary user', async () => {
  const fetchSpy = vi
    .spyOn(globalThis, 'fetch')
    .mockRejectedValue(new Error('Unexpected fetch'))
  const view = renderBrowser(ROLE.USER)

  await act(async () => {})

  expect(view.container).toBeEmptyDOMElement()
  expect(screen.queryByText('Training Data')).not.toBeInTheDocument()
  expect(adapter).not.toHaveBeenCalled()
  expect(fetchSpy).not.toHaveBeenCalled()
})

it.each(['network', 'business'] as const)(
  'distinguishes admin loading, %s failure, and a successful empty retry',
  async (failure) => {
    const user = userEvent.setup()
    let resolve!: (value: unknown) => void
    let reject!: (reason: Error) => void
    respond = () =>
      new Promise((done, fail) => {
        resolve = done
        reject = fail
      })
    renderBrowser()

    expect(screen.getByText('Loading...')).toBeVisible()
    expect(screen.queryByText('No data')).not.toBeInTheDocument()
    expect(screen.queryByText('Failed to load')).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Export raw JSONL' })
    ).toBeDisabled()
    await waitFor(() => expect(adapter).toHaveBeenCalledTimes(1))

    await act(async () => {
      if (failure === 'network') reject(new Error('Connection lost'))
      else resolve({ success: false, message: 'Capture storage unavailable' })
    })
    expect(await screen.findByText('Failed to load')).toBeVisible()
    expect(screen.queryByText('No data')).not.toBeInTheDocument()
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()

    respond = () => samples()
    await user.click(screen.getByRole('button', { name: 'Retry' }))

    expect(await screen.findByText('No data')).toBeVisible()
    expect(
      screen.getByText(
        /Historical unrecorded or expired data cannot be restored/
      )
    ).toBeVisible()
    expect(screen.queryByText('Failed to load')).not.toBeInTheDocument()
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Export OpenAI JSONL' })
    ).toBeDisabled()
  }
)

it('loads full stored input and output only after View, instead of reusing 200-byte previews', async () => {
  const user = userEvent.setup()
  const input = `${sample.input_preview} FULL STORED INPUT beyond the preview`
  const output = `${sample.output_preview} FULL STORED OUTPUT beyond the preview`
  let resolveContent!: (value: unknown) => void
  respond = (url) => {
    if (url.pathname === '/api/prompt-security/training-samples') {
      return samples([sample])
    }
    if (url.pathname === '/api/prompt-security/content') {
      return new Promise((resolve) => {
        resolveContent = resolve
      })
    }
    throw new Error(`Unexpected request: ${url}`)
  }
  renderBrowser()

  await screen.findByText(sample.input_preview)
  expect(screen.getByText(sample.output_preview)).toBeVisible()
  expect(requests).toHaveLength(1)
  expect(screen.queryByText(input)).not.toBeInTheDocument()

  await user.click(
    screen.getByRole('button', { name: `View content ${sample.request_id}` })
  )
  const dialog = await screen.findByRole('dialog', { name: 'Content details' })
  expect(within(dialog).getByText('Loading...')).toBeVisible()
  await waitFor(() => expect(requests).toHaveLength(2))
  expect(requests[1].pathname).toBe('/api/prompt-security/content')
  expect(requests[1].searchParams.get('request_id')).toBe(sample.request_id)
  await act(async () =>
    resolveContent({
      success: true,
      data: {
        items: [
          { direction: 'request', content: input },
          { direction: 'response', content: output },
        ],
      },
    })
  )

  expect(await within(dialog).findByText(input)).toBeVisible()
  expect(within(dialog).getByText(output)).toBeVisible()
  expect(
    within(dialog).getByRole('button', { name: 'Copy Input content' })
  ).toBeEnabled()
  expect(
    within(dialog).getByRole('button', { name: 'Copy Output content' })
  ).toBeEnabled()
  expect(
    within(dialog).queryByText(sample.input_preview, { exact: true })
  ).not.toBeInTheDocument()
})

it('applies model, user and second-based time filters only on Search and preserves them across pages', async () => {
  const user = userEvent.setup()
  respond = () => samples([sample], 21)
  renderBrowser()
  await screen.findByText(sample.input_preview)
  expect(Object.fromEntries(requests[0].searchParams)).toEqual({
    p: '1',
    page_size: '20',
  })
  expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()

  await user.click(screen.getByRole('button', { name: 'Next' }))
  await waitFor(() => expect(screen.getByText('2 / 2')).toBeVisible())
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Previous' })).toBeEnabled()
  )
  expect(requests.at(-1)?.searchParams.get('p')).toBe('2')
  expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()

  await user.type(
    screen.getByRole('textbox', { name: 'Model' }),
    '  gpt-4o + custom  '
  )
  await user.type(screen.getByRole('spinbutton', { name: 'User ID' }), '42')
  await user.click(screen.getByRole('button', { name: 'Date Range' }))
  const start = '2026-10-01T08:30:15'
  const end = '2026-10-02T09:45:30'
  fireEvent.change(screen.getByLabelText('Start Time'), {
    target: { value: start },
  })
  fireEvent.change(screen.getByLabelText('End Time'), {
    target: { value: end },
  })
  await user.click(screen.getByRole('button', { name: 'Confirm' }))
  expect(requests).toHaveLength(2)

  await user.click(screen.getByRole('button', { name: 'Search' }))
  const applied = {
    model_name: 'gpt-4o + custom',
    user_id: '42',
    start_timestamp: String(new Date(start).getTime() / 1000),
    end_timestamp: String(new Date(end).getTime() / 1000),
    p: '1',
    page_size: '20',
  }
  await waitFor(() => expect(requests).toHaveLength(3))
  expect(Object.fromEntries(requests[2].searchParams)).toEqual(applied)
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Next' })).toBeEnabled()
  )
  expect(screen.getByText('1 / 2')).toBeVisible()

  await user.clear(screen.getByRole('textbox', { name: 'Model' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Model' }),
    'unapplied-model'
  )
  await user.click(screen.getByRole('button', { name: 'Next' }))
  await waitFor(() => expect(requests).toHaveLength(4))
  expect(Object.fromEntries(requests[3].searchParams)).toEqual({
    ...applied,
    p: '2',
  })
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Previous' })).toBeEnabled()
  )

  await user.click(screen.getByRole('button', { name: 'Reset' }))
  await waitFor(() => expect(requests).toHaveLength(5))
  expect(Object.fromEntries(requests[4].searchParams)).toEqual({
    p: '1',
    page_size: '20',
  })
  expect(screen.getByRole('textbox', { name: 'Model' })).toHaveValue('')
  expect(screen.getByRole('spinbutton', { name: 'User ID' })).toHaveValue(null)
  expect(screen.getByRole('button', { name: 'Date Range' })).toBeVisible()
})
