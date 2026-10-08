import { describe, expect, it } from 'vitest'
import { formatRecordedClient, parseClientDevice } from '@/lib/client-device'

describe('recorded client identity', () => {
  const web = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/150.0.0.0 Safari/537.36'
  it('labels desktop requests as the app instead of Chromium', () => {
    const ua = `${web} AivoryDesktop/2.5.1-beta.6`
    expect(parseClientDevice(ua, 'App 版')).toMatchObject({ browser: 'App 版', desktop: true, mobile: false, os: 'macOS' })
    expect(formatRecordedClient(ua, 'App 版')).toBe('App 版 2.5.1-beta.6 · macOS')
  })
  it('preserves ordinary browser identification and raw web audit information', () => {
    expect(parseClientDevice(web, 'App 版')).toMatchObject({ browser: 'Chrome', desktop: false, os: 'macOS' })
    expect(formatRecordedClient(web, 'App 版')).toBe(web)
    expect(parseClientDevice('Mozilla/5.0 (Android) Chrome/150.0 Mobile', 'App 版').mobile).toBe(true)
    expect(parseClientDevice('Mozilla/5.0 Chrome/150.0 AivoryDesktopFake/2.0', 'App 版').desktop).toBe(false)
  })
})
