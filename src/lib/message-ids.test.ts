import { describe, expect, it } from 'vitest'
import { isKnownLocalMessageId, persistedMessageReference } from './message-ids'

describe('isKnownLocalMessageId', () => {
  it('identifies explicitly marked optimistic messages without relying on their prefix', () => {
    expect(isKnownLocalMessageId([{ id: 'msg_client_placeholder', localOnly: true }], 'msg_client_placeholder')).toBe(true)
  })

  it('does not reject a persisted id merely because it uses the local-id prefix', () => {
    expect(isKnownLocalMessageId([{ id: 'm_persisted_by_server' }], 'm_persisted_by_server')).toBe(false)
  })

  it('allows ids outside the loaded active path', () => {
    expect(isKnownLocalMessageId([{ id: 'msg_visible' }], 'msg_persisted_sibling')).toBe(false)
  })

  it('blocks the stopped turn local user id selected by an edit-resend branch switcher', () => {
    const stoppedTurnUserId = 'm_stopped_turn_user'
    const editedResendUserId = 'm_edited_resend_user'
    const generated = new Set([stoppedTurnUserId, editedResendUserId])
    // Before reconciliation, edit-resend seeds the new user bubble exactly this
    // way. From branch 2, the previous arrow selects the stopped turn's local id.
    const siblings = [stoppedTurnUserId, editedResendUserId]
    const branchIndex = 1
    const branchSwitcherTarget = siblings[branchIndex - 1]

    expect(branchSwitcherTarget).toBe(stoppedTurnUserId)
    expect(
      isKnownLocalMessageId(
        // truncateToParent has already removed the stopped row itself, so the
        // exact-id registry is required in addition to the current-row marker.
        [{ id: editedResendUserId, localOnly: true }],
        branchSwitcherTarget,
        generated,
      ),
    ).toBe(true)
  })

  it('never serializes an explicitly marked optimistic parent id', () => {
    expect(
      persistedMessageReference(
        [{ id: 'm_local_parent', localOnly: true }],
        'm_local_parent',
      ),
    ).toBeUndefined()
  })

  it('preserves persisted parents and treats an empty branch parent as a root', () => {
    expect(persistedMessageReference([{ id: 'msg_parent' }], 'msg_parent')).toBe('msg_parent')
    expect(persistedMessageReference([{ id: 'msg_parent' }], '')).toBeUndefined()
  })
})
