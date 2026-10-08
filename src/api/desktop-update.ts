import { api } from './client'
import type { SystemUpdateRelease } from './system-update'

export const DESKTOP_PLATFORMS = ['windows_x64', 'windows_arm64', 'macos_arm64', 'macos_x64', 'linux_x64', 'linux_arm64'] as const
export type DesktopPlatform = typeof DESKTOP_PLATFORMS[number]

export interface DesktopUpdateConfig {
  enabled: boolean
  source?: 'custom' | 'official'
  version: string
  downloads: Partial<Record<DesktopPlatform, string>>
}

export interface DesktopDownloadConfig {
  enabled: boolean
  url?: string
}

export function desktopDownloadConfig() {
  return api<DesktopDownloadConfig>('/public/desktop-download', { activity: 'background' })
}

export function notifyDesktopDownloadChanged() {
  window.dispatchEvent(new Event('aivory:desktop-download-changed'))
}

export interface DesktopUpdateState {
  config: DesktopUpdateConfig
  latest_version?: string
  update_available: boolean
  releases?: (SystemUpdateRelease & { downloads?: Partial<Record<DesktopPlatform, string>> })[]
  check_error?: string
}

export const desktopUpdateApi = {
  state: () => api<DesktopUpdateState>('/admin/desktop-update', { activity: 'background' }),
  check: () => api<DesktopUpdateState>('/admin/desktop-update/check', { method: 'POST', activity: 'background' }),
}

export function notifyDesktopUpdateChanged() {
  window.dispatchEvent(new Event('aivory:desktop-update-changed'))
}
