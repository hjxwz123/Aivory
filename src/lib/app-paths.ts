const CHAT_SHELL_PREFIXES = ['/chat', '/projects', '/files', '/skills', '/kb', '/subscription'] as const

/** Routes rendered inside ChatLayout and therefore needing its sidebar caches/realtime stream. */
export function isChatShellPath(pathname: string): boolean {
  if (pathname === '/') return true
  return CHAT_SHELL_PREFIXES.some((prefix) => pathname === prefix || pathname.startsWith(`${prefix}/`))
}
