/**
 * Passkey (WebAuthn) client helpers. The server hands out option documents
 * serialized by go-webauthn (base64url strings); this module translates them
 * to/from what `navigator.credentials` expects, and serializes the resulting
 * credential back to the exact JSON shape the server parses — so the wire
 * format round-trips without any third-party library.
 *
 * Passkeys require a secure context: HTTPS, or http://localhost / 127.0.0.1.
 * Everything here degrades to a typed PasskeyError instead of throwing raw
 * DOMExceptions, so callers can map codes to i18n strings.
 */
import type { PasskeyJson } from '@/api/types'

export class PasskeyError extends Error {
  constructor(public readonly code: 'passkey_unavailable' | 'passkey_cancelled') {
    super(code)
    this.name = 'PasskeyError'
  }
}

export function isPasskeyAvailable(): boolean {
  return typeof window !== 'undefined'
    && window.isSecureContext
    && !!navigator.credentials
    && typeof (globalThis as { PublicKeyCredential?: unknown }).PublicKeyCredential !== 'undefined'
}

export function bufferToBase64Url(input: ArrayBuffer | Uint8Array): string {
  const bytes = input instanceof Uint8Array ? input : new Uint8Array(input)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

export function base64UrlToBuffer(value: string): ArrayBuffer {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized + '='.repeat((4 - (normalized.length % 4)) % 4)
  const binary = atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index)
  return bytes.buffer
}

function publicKeySection(options: PasskeyJson): Record<string, unknown> {
  const publicKey = (options as { publicKey?: unknown }).publicKey
  if (!publicKey || typeof publicKey !== 'object') throw new PasskeyError('passkey_unavailable')
  const section = publicKey as Record<string, unknown>
  if (typeof section.challenge !== 'string') throw new PasskeyError('passkey_unavailable')
  return section
}

function credentialDescriptors(value: unknown): PublicKeyCredentialDescriptor[] {
  return Array.isArray(value)
    ? value.map((item) => {
        const descriptor = item as { id: string; type?: string }
        return { type: 'public-key', id: base64UrlToBuffer(descriptor.id) }
      })
    : []
}

function toCreationOptions(options: PasskeyJson): PublicKeyCredentialCreationOptions {
  const section = publicKeySection(options)
  const user = section.user as { id: string; name?: string; displayName?: string }
  return {
    challenge: base64UrlToBuffer(String(section.challenge)),
    rp: section.rp as PublicKeyCredentialRpEntity,
    user: {
      id: base64UrlToBuffer(user.id),
      name: user.name ?? '',
      displayName: user.displayName ?? user.name ?? '',
    },
    pubKeyCredParams: (section.pubKeyCredParams as PublicKeyCredentialParameters[] | undefined)?.length
      ? section.pubKeyCredParams as PublicKeyCredentialParameters[]
      : [{ type: 'public-key', alg: -7 }, { type: 'public-key', alg: -257 }],
    timeout: typeof section.timeout === 'number' ? section.timeout : 60_000,
    excludeCredentials: credentialDescriptors(section.excludeCredentials),
    authenticatorSelection: section.authenticatorSelection as AuthenticatorSelectionCriteria | undefined,
    attestation: section.attestation as AttestationConveyancePreference | undefined,
  }
}

function toRequestOptions(options: PasskeyJson): PublicKeyCredentialRequestOptions {
  const section = publicKeySection(options)
  // Discoverable (usernameless) login: the server sends an empty allow list and
  // the browser picks the matching passkey itself.
  return {
    challenge: base64UrlToBuffer(String(section.challenge)),
    timeout: typeof section.timeout === 'number' ? section.timeout : 60_000,
    allowCredentials: credentialDescriptors(section.allowCredentials),
    userVerification: (section.userVerification as UserVerificationRequirement | undefined) ?? 'required',
  }
}

/** Serialize a WebAuthn credential to the base64url JSON the server parses. */
export function serializePasskeyCredential(credential: PublicKeyCredential): PasskeyJson {
  const response = credential.response
  const serialized: Record<string, unknown> = {
    id: credential.id,
    rawId: credential.rawId ? bufferToBase64Url(credential.rawId) : credential.id,
    type: credential.type,
  }
  // Duck typing instead of `instanceof Authenticator*Response`: some test and
  // embedded-webview environments lack those globals.
  if ('attestationObject' in response) {
    const attestation = response as AuthenticatorAttestationResponse
    serialized.response = {
      clientDataJSON: bufferToBase64Url(attestation.clientDataJSON),
      attestationObject: bufferToBase64Url(attestation.attestationObject),
      transports: typeof attestation.getTransports === 'function' ? attestation.getTransports() : [],
    }
  } else if ('signature' in response) {
    const assertion = response as AuthenticatorAssertionResponse
    serialized.response = {
      clientDataJSON: bufferToBase64Url(assertion.clientDataJSON),
      authenticatorData: bufferToBase64Url(assertion.authenticatorData),
      signature: bufferToBase64Url(assertion.signature),
      userHandle: assertion.userHandle ? bufferToBase64Url(assertion.userHandle) : null,
    }
  } else {
    throw new PasskeyError('passkey_unavailable')
  }
  return serialized
}

async function runCeremony(get: () => Promise<Credential | null>): Promise<PasskeyJson> {
  if (!isPasskeyAvailable()) throw new PasskeyError('passkey_unavailable')
  let credential: Credential | null
  try {
    credential = await get()
  } catch (cause) {
    // User dismissed the biometric sheet / it timed out — a distinct, retryable
    // state from "this device has no passkey support".
    if (cause instanceof DOMException && cause.name === 'NotAllowedError') throw new PasskeyError('passkey_cancelled')
    throw new PasskeyError('passkey_unavailable')
  }
  if (!credential || !(credential instanceof PublicKeyCredential)) throw new PasskeyError('passkey_unavailable')
  return serializePasskeyCredential(credential)
}

/** Registration ceremony (called after /me/passkeys/begin returned options). */
export function createPasskeyCredential(options: PasskeyJson): Promise<PasskeyJson> {
  const publicKey = toCreationOptions(options)
  return runCeremony(() => navigator.credentials.create({ publicKey }))
}

/** Assertion ceremony (called after /auth/passkey/begin returned options). */
export function requestPasskeyAssertion(options: PasskeyJson): Promise<PasskeyJson> {
  const publicKey = toRequestOptions(options)
  return runCeremony(() => navigator.credentials.get({ publicKey }))
}
