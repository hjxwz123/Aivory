import { api } from './client'

export type SystemUpdateJobStatus = 'idle' | 'pulling' | 'restarting' | 'checking' | 'completed' | 'failed'

export interface SystemUpdateJob {
  id?: string
  status: SystemUpdateJobStatus
  progress?: string
  version?: string
  error?: string
  started_at?: number
  completed_at?: number
}

export interface SystemUpdateRelease {
  version: string
  name?: string
  notes?: string
  url?: string
  published_at?: string
  prerelease: boolean
  installable: boolean
}

export interface SystemUpdateState {
  current_version: string
  latest_version?: string
  update_available: boolean
  configured: boolean
  release_name?: string
  release_notes?: string
  release_url?: string
  published_at?: string
  releases?: SystemUpdateRelease[]
  check_error?: string
  updater_error?: string
  job?: SystemUpdateJob
}

export const systemUpdateApi = {
  state: (activity: 'foreground' | 'background' = 'background') =>
    api<SystemUpdateState>('/admin/system-update', { activity }),
  check: () => api<SystemUpdateState>('/admin/system-update/check', { method: 'POST' }),
  start: (version: string) =>
    api<{ job: SystemUpdateJob }>('/admin/system-update/start', {
      method: 'POST',
      body: { version },
    }),
}
