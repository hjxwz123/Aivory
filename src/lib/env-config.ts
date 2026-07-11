/**
 * Optional build-time overrides for values that are otherwise hardcoded
 * frontend defaults (poll intervals, page sizes, client-side caps, …).
 *
 * Vite inlines `VITE_*` variables at BUILD time, so these are set in the build
 * environment (before `npm run build` / `vite build`), not at runtime. Each
 * getter returns the supplied default when the variable is unset/empty/invalid,
 * so a normal build with no extra env behaves exactly as before.
 *
 * These are intentionally NOT in .env.example — see docs/config-reference.md
 * for the full list and add the ones you need.
 */

const env = import.meta.env as Record<string, string | undefined>

/** Numeric override for `key` (e.g. a millisecond delay or page size), else `def`. */
export function envNum(key: string, def: number): number {
  const raw = env[key]
  if (raw == null || raw === '') return def
  const n = Number(raw)
  return Number.isFinite(n) ? n : def
}

/** String override for `key`, else `def`. */
export function envStr(key: string, def: string): string {
  const raw = env[key]
  return raw == null || raw === '' ? def : raw
}

/** Boolean override for `key` (1/true/yes/on ⇒ true), else `def`. */
export function envBool(key: string, def: boolean): boolean {
  const raw = env[key]
  if (raw == null || raw === '') return def
  const v = raw.toLowerCase()
  if (v === '1' || v === 'true' || v === 'yes' || v === 'on') return true
  if (v === '0' || v === 'false' || v === 'no' || v === 'off') return false
  return def
}
