export function parseClientDevice(userAgent: string, appLabel: string) {
  const appVersion = /(?:^|\s)AivoryDesktop\/([^\s]+)/.exec(userAgent)?.[1] ?? ''
  const desktop = Boolean(appVersion)
  const mobile = !desktop && /Mobile|Android|iPhone|iPad|iPod/i.test(userAgent)
  let os = ''
  if (/iPhone|iPad|iPod/i.test(userAgent)) os = 'iOS'
  else if (/Android/i.test(userAgent)) os = 'Android'
  else if (/Windows/i.test(userAgent)) os = 'Windows'
  else if (/Mac OS X|Macintosh/i.test(userAgent)) os = 'macOS'
  else if (/CrOS/i.test(userAgent)) os = 'ChromeOS'
  else if (/Linux/i.test(userAgent)) os = 'Linux'
  let browser = ''
  if (desktop) browser = appLabel
  else if (/Edg\//i.test(userAgent)) browser = 'Edge'
  else if (/OPR\/|Opera/i.test(userAgent)) browser = 'Opera'
  else if (/Firefox\//i.test(userAgent)) browser = 'Firefox'
  else if (/Chrome\//i.test(userAgent)) browser = 'Chrome'
  else if (/Safari\//i.test(userAgent)) browser = 'Safari'
  return { browser, os, mobile, desktop, appVersion, label: [browser, os].filter(Boolean).join(' · ') }
}

export function formatRecordedClient(userAgent: string, appLabel: string) {
  const client = parseClientDevice(userAgent, appLabel)
  return client.desktop ? [`${client.browser} ${client.appVersion}`, client.os].filter(Boolean).join(' · ') : userAgent
}
