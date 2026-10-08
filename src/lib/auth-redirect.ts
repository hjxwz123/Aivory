/** Only follow an internal route after login, including native browser consent. */
// eslint-disable-next-line no-control-regex -- Control characters and backslashes must not alter URL parsing.
const unsafePathCharacters = /[\\\u0000-\u001f\u007f]/

export function safeAuthRedirect(from: unknown): string {
  return typeof from === 'string' && from.startsWith('/') && !from.startsWith('//')
    && !unsafePathCharacters.test(from) ? from : '/'
}
