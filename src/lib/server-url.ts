/** Public links must reference the deployment, including in the local desktop UI. */
export function serverOrigin(): string {
  return window.aivoryDesktop?.serverBaseUrl || window.location.origin
}

export function publicServerUrl(path: string): string {
  return new URL(path, serverOrigin()).href
}
