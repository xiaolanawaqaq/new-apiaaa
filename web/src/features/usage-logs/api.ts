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
import { api, getFreshAuthHeaders, type ApiRequestConfig } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

import type { UsageLog } from './data/schema'
import { buildQueryParams } from './lib/query-params'
import { parseTaskArtifactsResponse } from './lib/task-artifacts'
import type {
  ClientAgentLogsResponse,
  ClientAgentUsageResponse,
  ClientAgentUsageTrendResponse,
  GetLogsParams,
  GetLogsResponse,
  GetLogStatsParams,
  GetLogStatsResponse,
  GetMidjourneyLogsParams,
  GetTaskLogsParams,
  LogContentResponse,
  TaskArtifactsResponse,
  TrainingSampleFilters,
  TrainingSamplesResponse,
  UserInfo,
} from './types'

// ============================================================================
// Generic API Helpers
// ============================================================================

function buildApiPath(endpoint: string, isAdmin: boolean): string {
  return isAdmin ? endpoint : `${endpoint}/self`
}

async function fetchLogs<T>(
  endpoint: string,
  params: T,
  isAdmin: boolean
): Promise<GetLogsResponse> {
  const paramRecord = params as unknown as Record<string, unknown>
  const queryParams = buildQueryParams({
    p: paramRecord.p || 1,
    page_size: paramRecord.page_size || 20,
    ...params,
  })
  const path = buildApiPath(endpoint, isAdmin)
  const res = await api.get(`${path}?${queryParams}`)
  return res.data
}

async function fetchLogStats<T>(
  endpoint: string,
  params: T,
  isAdmin: boolean
): Promise<GetLogStatsResponse> {
  const queryParams = buildQueryParams(
    params as unknown as Record<string, unknown>
  )
  const path = buildApiPath(endpoint, isAdmin)
  const res = await api.get(`${path}/stat?${queryParams}`)
  return res.data
}

// ============================================================================
// Common Log APIs
// ============================================================================

export const getAllLogs = (params: GetLogsParams = {}) =>
  fetchLogs('/api/log', params, true)

export const getUserLogs = (
  params: Omit<GetLogsParams, 'username' | 'channel'> = {}
) => fetchLogs('/api/log', params, false)

export const getLogStats = (params: GetLogStatsParams = {}) =>
  fetchLogStats('/api/log', params, true)

export async function getClientAgentUsage(
  params: GetLogsParams = {}
): Promise<ClientAgentUsageResponse> {
  const queryParams = buildQueryParams(
    params as unknown as Record<string, unknown>
  )
  const res = await api.get(`/api/log/client_usage?${queryParams}`)
  return res.data
}

export async function getClientAgentUsageLogs(
  params: GetLogsParams & {
    client_agent?: string
    user_agent?: string
    unknown?: string
  } = {}
): Promise<ClientAgentLogsResponse> {
  const queryParams = buildQueryParams(
    params as unknown as Record<string, unknown>
  )
  const res = await api.get(`/api/log/client_usage/logs?${queryParams}`)
  return res.data
}

export async function getClientAgentUsageTrend(
  params: GetLogsParams & {
    client_agent?: string
    user_agent?: string
    unknown?: string
    bucket_seconds?: number
  } = {}
): Promise<ClientAgentUsageTrendResponse> {
  const queryParams = buildQueryParams(
    params as unknown as Record<string, unknown>
  )
  const res = await api.get(`/api/log/client_usage/trend?${queryParams}`)
  return res.data
}

// Captured request/response text for one log entry, keyed by request_id.
export async function getLogContent(
  requestId: string
): Promise<LogContentResponse> {
  const queryParams = buildQueryParams({
    request_id: requestId,
    page_size: 20,
  })
  const res = await api.get(`/api/prompt-security/content?${queryParams}`)
  return res.data
}

export async function getTrainingSamples(
  params: TrainingSampleFilters & { p?: number; page_size?: number } = {}
): Promise<TrainingSamplesResponse> {
  const queryParams = buildQueryParams({ ...params })
  const res = await api.get(
    `/api/prompt-security/training-samples?${queryParams}`
  )
  return res.data
}

export async function exportTrainingData(
  format: 'raw' | 'openai',
  params: TrainingSampleFilters,
  signal: AbortSignal
): Promise<{ blob: Blob; count: number }> {
  const queryParams = buildQueryParams({ format, ...params })
  const response = await fetch(
    `/api/prompt-security/content/export?${queryParams}`,
    { credentials: 'include', headers: await getFreshAuthHeaders(), signal }
  )
  if (!response.ok || !response.body) {
    const body: unknown = await response.json().catch(() => null)
    throw createServerError(body ?? { status: response.status })
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder('utf-8', { fatal: true })
  const lines: string[] = []
  let buffer = ''
  let bytes = 0
  let count: number | undefined
  let completed = false
  try {
    for (;;) {
      const { done, value } = await reader.read()
      bytes += value?.byteLength ?? 0
      // Browser downloads buffer the file; bound memory independently of the server stream.
      if (bytes > 128 * 1024 * 1024) {
        throw createServerError({
          message: 'Export exceeds 128 MiB. Narrow the filters and try again.',
        })
      }
      buffer += done
        ? decoder.decode()
        : decoder.decode(value, { stream: true })
      const chunkLines = buffer.split('\n')
      buffer = done ? '' : (chunkLines.pop() ?? '')
      for (const line of chunkLines) {
        if (!line.trim()) continue
        let parsed: unknown
        try {
          parsed = JSON.parse(line)
        } catch {
          throw createServerError({ message: 'Failed to export content' })
        }
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          throw createServerError({ message: 'Failed to export content' })
        }
        if (count !== undefined) {
          throw createServerError({ message: 'Failed to export content' })
        }
        if ('success' in parsed) {
          if (parsed.success !== true || !('data' in parsed)) {
            throw createServerError(parsed)
          }
          const data = parsed.data
          if (
            !data ||
            typeof data !== 'object' ||
            !('count' in data) ||
            typeof data.count !== 'number' ||
            !Number.isSafeInteger(data.count) ||
            data.count < 0
          ) {
            throw createServerError({ message: 'Failed to export content' })
          }
          count = data.count
        } else {
          if (
            (format === 'openai' &&
              (!('messages' in parsed) || !Array.isArray(parsed.messages))) ||
            (format === 'raw' &&
              (!('request_id' in parsed) ||
                typeof parsed.request_id !== 'string'))
          ) {
            throw createServerError({ message: 'Failed to export content' })
          }
          lines.push(`${line}\n`)
        }
      }
      if (done) break
    }
    if (count === undefined || count !== lines.length) {
      throw createServerError({ message: 'Failed to export content' })
    }
    completed = true
    // The API result trailer is not a training sample and must not reach the file.
    return {
      blob: new Blob(lines, { type: 'application/x-ndjson;charset=utf-8' }),
      count,
    }
  } finally {
    if (!completed) await reader.cancel().catch(() => undefined)
    reader.releaseLock()
  }
}

export const getUserLogStats = (
  params: Omit<GetLogStatsParams, 'username' | 'channel'> = {}
) => fetchLogStats('/api/log', params, false)

/**
 * Stream the logs matching params, newest first. The server sends one JSON
 * log per line and ends with an API response object; a stream that ends
 * without a successful one is a failed export. Resolves with the log count.
 */
export async function exportLogs(
  params: GetLogsParams,
  isAdmin: boolean,
  options: { signal: AbortSignal; onLog: (log: UsageLog) => void }
): Promise<number> {
  const queryParams = buildQueryParams(
    params as unknown as Record<string, unknown>
  )
  const response = await fetch(
    `${buildApiPath('/api/log', isAdmin)}/export?${queryParams}`,
    {
      credentials: 'include',
      headers: await getFreshAuthHeaders(),
      signal: options.signal,
    }
  )
  if (!response.ok || !response.body) {
    const body: unknown = await response.json().catch(() => null)
    throw createServerError(body ?? { status: response.status })
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let result: { success?: unknown; data?: { count?: number } } | null = null
  for (;;) {
    const { done, value } = await reader.read()
    buffer += done ? decoder.decode() : decoder.decode(value, { stream: true })
    const lines = buffer.split('\n')
    buffer = done ? '' : (lines.pop() ?? '')
    for (const line of lines) {
      if (!line.trim()) continue
      let parsed
      try {
        parsed = JSON.parse(line)
      } catch {
        throw createServerError(null)
      }
      if (typeof parsed.success === 'boolean') result = parsed
      else options.onLog(parsed as UsageLog)
    }
    if (done) break
  }
  if (result?.success !== true) {
    throw createServerError(result)
  }
  return result.data?.count ?? 0
}

export async function getUserInfo(
  userId: number
): Promise<{ success: boolean; message?: string; data?: UserInfo }> {
  const res = await api.get(`/api/user/${userId}`)
  return res.data
}

// ============================================================================
// MjProxy (Drawing) Logs API
// ============================================================================

export const getAllMidjourneyLogs = (params: GetMidjourneyLogsParams) =>
  fetchLogs('/api/mj', params, true)

export const getUserMidjourneyLogs = (params: GetMidjourneyLogsParams) =>
  fetchLogs('/api/mj', params, false)

// ============================================================================
// Task Logs API
// ============================================================================

export const getAllTaskLogs = (params: GetTaskLogsParams) =>
  fetchLogs('/api/task', params, true)

export const getUserTaskLogs = (params: GetTaskLogsParams) =>
  fetchLogs('/api/task', params, false)

const taskArtifactRequestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
} satisfies ApiRequestConfig

export async function getTaskArtifacts(taskId: string) {
  const response = await api.get<TaskArtifactsResponse>(
    `/api/task/${encodeURIComponent(taskId)}/artifacts`,
    taskArtifactRequestConfig
  )
  return parseTaskArtifactsResponse(response.data)
}
