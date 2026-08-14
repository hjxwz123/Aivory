/**
 * Normalize a user-discovery query without accepting names or partial emails.
 * The server remains authoritative, but this prevents premature lookups while
 * someone is still typing an address.
 */
export function normalizeExactUserEmailQuery(value: string): string | null {
  const email = value.trim().toLowerCase()
  if (email.length === 0 || email.length > 320 || !/^[^\s@]+@[^\s@]+$/.test(email)) {
    return null
  }
  return email
}
