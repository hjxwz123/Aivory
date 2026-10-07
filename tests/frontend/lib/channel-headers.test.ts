import { describe, expect, it } from 'vitest'
import { parseChannelHeaders } from '@/lib/channel-headers'

describe('channel request headers', () => {
  it('accepts string values and clears custom headers with a blank object or input', () => {
    expect(parseChannelHeaders('{"A":"a","X-Tenant":"team"}')).toEqual({ headers: { A: 'a', 'X-Tenant': 'team' } })
    expect(parseChannelHeaders('{}')).toEqual({ headers: {} })
    expect(parseChannelHeaders(' ')).toEqual({ headers: {} })
  })

  it.each([
    ['{', 'invalidJson'], ['null', 'invalidObject'], ['[]', 'invalidObject'],
    ['{"A":1}', 'invalidValue'], ['{"A":null}', 'invalidValue'],
    ['{"A":"a\\nb"}', 'invalidValue'], ['{"bad name":"a"}', 'invalidName'],
    ['{"A":"a","a":"b"}', 'duplicateName'], ['{"content-length":"5"}', 'transportManaged'],
  ])('rejects invalid configuration %s', (text, error) => {
    expect(parseChannelHeaders(text)).toEqual({ error })
  })
})
