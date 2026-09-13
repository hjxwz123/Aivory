import { describe, expect, it } from 'vitest'
import {
  bufferToBase64Url,
  base64UrlToBuffer,
  serializePasskeyCredential,
} from '@/lib/passkey'

function bytes(value: number[]): ArrayBuffer {
  return new Uint8Array(value).buffer
}

describe('passkey base64url helpers', () => {
  it('round-trips arbitrary bytes without +/= characters', () => {
    const raw = bytes([0, 1, 253, 254, 255, 63, 62, 9, 10, 13])
    const encoded = bufferToBase64Url(raw)
    expect(encoded).not.toMatch(/[+/=]/)
    expect(new Uint8Array(base64UrlToBuffer(encoded))).toEqual(new Uint8Array(raw))
  })

  it('decodes unpadded base64url of every length remainder', () => {
    for (let length = 0; length < 8; length++) {
      const raw = bytes(Array.from({ length }, (_, index) => index * 37))
      expect(new Uint8Array(base64UrlToBuffer(bufferToBase64Url(raw)))).toEqual(new Uint8Array(raw))
    }
  })
})

describe('serializePasskeyCredential', () => {
  it('serializes an attestation response to the server wire shape', () => {
    const credential = {
      id: 'abc',
      rawId: bytes([1, 2, 3]),
      type: 'public-key',
      response: {
        clientDataJSON: bytes([10, 11]),
        attestationObject: bytes([12, 13]),
        getTransports: () => ['internal', 'hybrid'],
      },
    }
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const json = serializePasskeyCredential(credential as any) as Record<string, any>
    expect(json.id).toBe('abc')
    expect(json.rawId).toBe('AQID')
    expect(json.response.clientDataJSON).toBe('Cgs')
    expect(json.response.attestationObject).toBe('DA0')
    expect(json.response.transports).toEqual(['internal', 'hybrid'])
  })

  it('serializes an assertion response with a null user handle', () => {
    const credential = {
      id: 'xyz',
      rawId: bytes([7]),
      type: 'public-key',
      response: {
        clientDataJSON: bytes([1]),
        authenticatorData: bytes([2]),
        signature: bytes([3]),
        userHandle: null,
      },
    }
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const json = serializePasskeyCredential(credential as any) as Record<string, any>
    expect(json.response.userHandle).toBeNull()
    expect(json.response.authenticatorData).toBe('Ag')
    expect(json.response.signature).toBe('Aw')
  })
})
