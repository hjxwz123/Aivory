package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"aurelia/server/internal/store"
)

// Public read-only conversation sharing (§ sharing). The owner creates a share
// (snapshotting the current active path), can revoke it, and the snapshot is
// served to anyone with the token — no auth, no cost, no private fields.

// publicShareMessage is the cost-stripped, identity-free message shape frozen
// into a share snapshot and returned to public viewers. Attachments ride along
// (id/filename/kind/url only — nothing sensitive) so shared conversations keep
// their uploaded images/files; the viewer fetches the bytes through the
// share-scoped public asset routes below.
type publicShareMessage struct {
	Role        string          `json:"role"`
	Blocks      json.RawMessage `json:"blocks"`
	Citations   json.RawMessage `json:"citations"`
	Attachments json.RawMessage `json:"attachments"`
	CreatedAt   int64           `json:"created_at"`
}

// shareInfo is the owner-facing share descriptor (no snapshot payload).
type shareInfo struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
}

// createShareHandler snapshots the conversation's active path and returns the
// public token. Re-sharing replaces any previous snapshot.
func createShareHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	conv, err := store.GetConversation(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	msgs, err := store.ListMessages(r.Context(), d.DB, conv.ID, conv.ActiveLeafID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	snap := make([]publicShareMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		blocks := m.Blocks
		if len(blocks) == 0 {
			blocks = json.RawMessage("[]")
		}
		cites := m.Citations
		if len(cites) == 0 {
			cites = json.RawMessage("[]")
		}
		// backfill gives pre-URL-era attachment rows their /api/files/<id> URL —
		// the share page rewrites those to the share-scoped public asset route.
		atts := backfillAttachmentURLs(m.Attachments)
		if len(atts) == 0 {
			atts = json.RawMessage("[]")
		}
		snap = append(snap, publicShareMessage{Role: m.Role, Blocks: blocks, Citations: cites, Attachments: atts, CreatedAt: m.CreatedAt})
	}
	payload, _ := json.Marshal(snap)
	share, err := store.CreateShare(r.Context(), d.DB, u.ID, conv.ID, conv.Title, payload)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, shareInfo{ID: share.ID, CreatedAt: share.CreatedAt})
}

// getShareHandler reports the current share for a conversation (or null).
func getShareHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	share, err := store.GetShareByConversation(r.Context(), d.DB, id, u.ID)
	if err != nil {
		// Not shared — return an explicit null so the client can distinguish
		// "no share" from a transport error.
		writeJSON(w, 200, map[string]any{"share": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"share": shareInfo{ID: share.ID, CreatedAt: share.CreatedAt}})
}

// deleteShareHandler revokes a conversation's public share.
func deleteShareHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	if err := store.DeleteShareByConversation(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// publicSharedHandler serves a share snapshot to anyone with the token. No auth.
func publicSharedHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	token := pathParam(r, "token")
	share, err := store.GetShareByToken(r.Context(), d.DB, token)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, map[string]any{
		"title":      share.Title,
		"messages":   share.Snapshot,
		"created_at": share.CreatedAt,
	})
}

// shareSnapshotHasID reports whether a share's frozen snapshot references the
// given asset id. This is the ACCESS CHECK for the public asset routes: a token
// can only ever expose files/artifacts of the conversation it snapshots.
//
// A byte scan avoids re-parsing the whole snapshot per asset request, but it's
// deliberately NARROWER than a raw contains: legit references always appear
// either as a quoted JSON id ("id":"file_x", "file_ref":"art_x") or as a URL
// path segment (/api/files/file_x) — requiring one of those shapes keeps an id
// merely PASTED into the shared conversation's text from authorising a fetch of
// someone else's file.
func shareSnapshotHasID(snapshot []byte, id string) bool {
	if len(id) < 8 {
		return false
	}
	return bytes.Contains(snapshot, []byte(`"`+id+`"`)) || bytes.Contains(snapshot, []byte("/"+id))
}

// publicSharedFileHandler streams an uploaded attachment referenced by a share
// snapshot to anyone with the share token (§ sharing). No auth — the private
// /api/files/:id route requires the OWNER's session, which a share viewer
// doesn't have; membership in the snapshot is the authorisation instead.
func publicSharedFileHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	token := pathParam(r, "token")
	id := pathParam(r, "id")
	share, err := store.GetShareByToken(r.Context(), d.DB, token)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	if !shareSnapshotHasID(share.Snapshot, id) {
		writeError(w, 404, errNotFound)
		return
	}
	f, err := store.GetFile(r.Context(), d.DB, id, "") // any owner: snapshot membership authorises
	if err != nil || f == nil {
		writeError(w, 404, errNotFound)
		return
	}
	serveStoredFile(d, w, f)
}

// publicSharedArtifactHandler is the artifact (generated image / produced file)
// twin of publicSharedFileHandler.
func publicSharedArtifactHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	token := pathParam(r, "token")
	id := pathParam(r, "id")
	share, err := store.GetShareByToken(r.Context(), d.DB, token)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	if !shareSnapshotHasID(share.Snapshot, id) {
		writeError(w, 404, errNotFound)
		return
	}
	a, err := store.GetArtifact(r.Context(), d.DB, id, "") // any owner: snapshot membership authorises
	if err != nil || a == nil {
		writeError(w, 404, errNotFound)
		return
	}
	serveStoredArtifact(d, w, a)
}
