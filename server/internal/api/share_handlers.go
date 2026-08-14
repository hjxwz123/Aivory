package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aivory/server/internal/msgcache"
	"aivory/server/internal/store"
)

// Public read-only conversation sharing (§ sharing). The owner creates a share
// (snapshotting the current active path), can revoke it, and the snapshot is
// served to anyone with the token — no auth, no cost, no private fields.

// publicShareMessage is the cost-stripped message shape frozen into a share
// snapshot and returned to public viewers. It carries only the display identity
// needed by the transcript (author name/avatar or model label/icon), never user
// ids, email addresses, provider details, or billing data. Attachments ride
// along (id/filename/kind/url only — nothing sensitive) so shared conversations
// keep their uploaded images/files; the viewer fetches the bytes through the
// share-scoped public asset routes below.
type publicShareMessage struct {
	Role         string          `json:"role"`
	Blocks       json.RawMessage `json:"blocks"`
	Citations    json.RawMessage `json:"citations"`
	Attachments  json.RawMessage `json:"attachments"`
	CreatedAt    int64           `json:"created_at"`
	AuthorName   string          `json:"author_name,omitempty"`
	AuthorAvatar string          `json:"author_avatar,omitempty"`
	ModelLabel   string          `json:"model_label,omitempty"`
	ModelIcon    string          `json:"model_icon,omitempty"`
	Fast         bool            `json:"fast,omitempty"`
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
	allowed, err := store.CanManageConversationShare(r.Context(), d.DB, conv.ID, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !allowed {
		writeError(w, 404, errNotFound)
		return
	}
	msgs, err := store.ListMessages(r.Context(), d.DB, conv.ID, conv.ActiveLeafID)
	if err != nil {
		writeError(w, 500, err)
		return
	}

	// Resolve display identities once before building the snapshot. Legacy user
	// rows have no author_id; the conversation creator is their implied author.
	// Likewise, very old assistant rows may rely on the conversation model id.
	authorIDs := map[string]struct{}{}
	modelIDs := map[string]struct{}{}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			authorID := strings.TrimSpace(m.AuthorID)
			if authorID == "" {
				authorID = conv.UserID
			}
			if authorID != "" {
				authorIDs[authorID] = struct{}{}
			}
		case "assistant":
			if m.Fast {
				continue
			}
			modelID := strings.TrimSpace(m.ModelID)
			if modelID == "" {
				modelID = strings.TrimSpace(conv.ModelID)
			}
			if modelID != "" {
				modelIDs[modelID] = struct{}{}
			}
		}
	}
	authorIDList := make([]string, 0, len(authorIDs))
	for id := range authorIDs {
		authorIDList = append(authorIDList, id)
	}
	authors, err := store.UserIdentities(r.Context(), d.DB, authorIDList)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	type modelIdentity struct {
		Label string
		Icon  string
	}
	models := make(map[string]modelIdentity, len(modelIDs))
	for id := range modelIDs {
		model, modelErr := store.GetModel(r.Context(), d.DB, id)
		if errors.Is(modelErr, store.ErrNotFound) {
			continue
		}
		if modelErr != nil {
			writeError(w, 500, modelErr)
			return
		}
		models[id] = modelIdentity{Label: strings.TrimSpace(model.Label), Icon: strings.TrimSpace(model.Icon)}
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
		blocks = redactInternalMessageBlocks(blocks)
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
		sharedMessage := publicShareMessage{
			Role:        m.Role,
			Blocks:      blocks,
			Citations:   cites,
			Attachments: atts,
			CreatedAt:   m.CreatedAt,
		}
		if m.Role == "user" {
			authorID := strings.TrimSpace(m.AuthorID)
			if authorID == "" {
				authorID = conv.UserID
			}
			if identity, ok := authors[authorID]; ok {
				sharedMessage.AuthorName = strings.TrimSpace(identity.Name)
				sharedMessage.AuthorAvatar = strings.TrimSpace(identity.AvatarURL)
			}
		} else if m.Fast {
			// Fast-mode model identity is deliberately hidden on every user-facing
			// surface; the frontend renders the localized Fast label and generic icon.
			sharedMessage.Fast = true
		} else {
			sharedMessage.ModelLabel = strings.TrimSpace(m.ModelLabel)
			modelID := strings.TrimSpace(m.ModelID)
			if modelID == "" {
				modelID = strings.TrimSpace(conv.ModelID)
			}
			if identity, ok := models[modelID]; ok {
				if sharedMessage.ModelLabel == "" {
					sharedMessage.ModelLabel = identity.Label
				}
				sharedMessage.ModelIcon = identity.Icon
			}
		}
		snap = append(snap, sharedMessage)
	}
	payload, _ := json.Marshal(snap)
	share, err := store.CreateShare(r.Context(), d.DB, u.ID, conv.ID, conv.Title, payload)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, shareInfo{ID: share.ID, CreatedAt: share.CreatedAt})
}

// getShareHandler reports the current share for a conversation (or null).
func getShareHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	allowed, err := store.CanManageConversationShare(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !allowed {
		writeError(w, 404, errNotFound)
		return
	}
	share, err := store.GetShareByConversation(r.Context(), d.DB, id, u.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Not shared — return an explicit null so the client can distinguish
			// "no share" from a transport error.
			writeJSON(w, 200, map[string]any{"share": nil})
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"share": shareInfo{ID: share.ID, CreatedAt: share.CreatedAt}})
}

// deleteShareHandler revokes a conversation's public share.
func deleteShareHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	allowed, err := store.CanManageConversationShare(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !allowed {
		writeError(w, 404, errNotFound)
		return
	}
	if err := store.DeleteShareByConversation(r.Context(), d.DB, id, u.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
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
		"messages":   redactPublicShareSnapshot(share.Snapshot),
		"created_at": share.CreatedAt,
	})
}

// redactPublicShareSnapshot also protects snapshots created by older builds.
// Public reads and clones must not trust that the stored JSON passed the current
// creation-time redaction boundary.
func redactPublicShareSnapshot(raw json.RawMessage) json.RawMessage {
	var messages []publicShareMessage
	if json.Unmarshal(raw, &messages) != nil {
		return json.RawMessage("[]")
	}
	for i := range messages {
		messages[i].Blocks = redactInternalMessageBlocks(messages[i].Blocks)
	}
	redacted, err := json.Marshal(messages)
	if err != nil {
		return json.RawMessage("[]")
	}
	return redacted
}

// cloneSharedConversationHandler lets a signed-in viewer copy a public share
// snapshot into their own personal conversations. It deliberately clones the
// frozen, cost-stripped snapshot rather than the owner's live conversation, so
// no private later messages or admin-only fields cross account boundaries.
func cloneSharedConversationHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	token := pathParam(r, "token")
	share, err := store.GetShareByToken(r.Context(), d.DB, token)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	var snap []publicShareMessage
	if err := json.Unmarshal(redactPublicShareSnapshot(share.Snapshot), &snap); err != nil {
		writeError(w, 500, err)
		return
	}
	modelID := ""
	if raw, err := store.GetSetting(d.DB, "default_model_id"); err == nil {
		_ = json.Unmarshal(raw, &modelID)
	}
	title := strings.TrimSpace(share.Title)
	if title == "" {
		title = "Shared conversation"
	}
	conv, err := store.CreateConversation(r.Context(), d.DB, store.Conversation{
		UserID:  u.ID,
		Title:   title,
		ModelID: modelID,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	parentID := ""
	base := time.Now().Unix()
	for i, m := range snap {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		blocks := rewriteShareAssetURLsInRaw(token, redactInternalMessageBlocks(m.Blocks))
		atts := rewriteShareAssetURLsInRaw(token, m.Attachments)
		cites := normalizeJSONList(m.Citations)
		created, err := store.CreateMessage(r.Context(), d.DB, store.Message{
			ConversationID: conv.ID,
			ParentID:       parentID,
			Role:           m.Role,
			ModelID:        modelID,
			Blocks:         blocks,
			Attachments:    atts,
			Citations:      cites,
			Status:         "complete",
			AuthorID:       userMessageAuthor(m.Role, u.ID),
			CreatedAt:      base + int64(i),
		})
		if err != nil {
			_, _ = store.DeleteConversation(r.Context(), d.DB, conv.ID, u.ID)
			writeError(w, 500, err)
			return
		}
		parentID = created.ID
	}
	msgcache.Bump(d.Cache, conv.ID)
	publishUserEvent(d, r, u.ID, "conversation.created", conv.ID) // §23
	stripServerConvFields(conv)
	writeJSON(w, 201, conv)
}

func userMessageAuthor(role, userID string) string {
	if role == "user" {
		return userID
	}
	return ""
}

func normalizeJSONList(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("[]")
	}
	return raw
}

func rewriteShareAssetURLsInRaw(token string, raw json.RawMessage) json.RawMessage {
	raw = normalizeJSONList(raw)
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return raw
	}
	for _, item := range items {
		if rawURL, ok := item["url"].(string); ok {
			item["url"] = publicShareAssetURL(token, rawURL)
		}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return raw
	}
	return json.RawMessage(b)
}

func publicShareAssetURL(token, rawURL string) string {
	fileID := privateAssetID(rawURL, "/api/files/")
	if fileID != "" {
		return "/api/public/shared/" + url.PathEscape(token) + "/files/" + url.PathEscape(fileID)
	}
	artifactID := privateAssetID(rawURL, "/api/artifacts/")
	if artifactID != "" {
		return "/api/public/shared/" + url.PathEscape(token) + "/artifacts/" + url.PathEscape(artifactID)
	}
	return rawURL
}

func privateAssetID(rawURL, prefix string) string {
	if !strings.HasPrefix(rawURL, prefix) {
		return ""
	}
	id := strings.TrimPrefix(rawURL, prefix)
	if cut := strings.IndexAny(id, "?#/"); cut >= 0 {
		id = id[:cut]
	}
	return id
}

type shareAssetReference struct {
	ID        string `json:"id"`
	FileRef   string `json:"file_ref"`
	URL       string `json:"url"`
	Kind      string `json:"kind"`
	Artifacts []struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	} `json:"artifacts"`
}

// shareSnapshotReferencesAsset only accepts references in the fields that the
// server uses for attachments and artifact blocks. Message text, citations,
// tool input/output, and unrelated JSON fields never grant access. The public
// handlers pair this frozen structured reference with a database query scoped
// to share.ConversationID, so a forged cross-conversation reference also fails.
func shareSnapshotReferencesAsset(snapshot []byte, assetType, id string) bool {
	if id == "" {
		return false
	}
	var messages []publicShareMessage
	if err := json.Unmarshal(snapshot, &messages); err != nil {
		return false
	}
	for _, message := range messages {
		switch assetType {
		case "file":
			var attachments []shareAssetReference
			if json.Unmarshal(message.Attachments, &attachments) != nil {
				continue
			}
			for _, attachment := range attachments {
				if attachment.ID == id || privateAssetID(attachment.URL, "/api/files/") == id {
					return true
				}
			}
		case "artifact":
			var blocks []shareAssetReference
			if json.Unmarshal(message.Blocks, &blocks) != nil {
				continue
			}
			for _, block := range blocks {
				if block.Kind != "artifact" {
					continue
				}
				if block.ID == id || block.FileRef == id || privateAssetID(block.URL, "/api/artifacts/") == id {
					return true
				}
				for _, artifact := range block.Artifacts {
					if artifact.ID == id || privateAssetID(artifact.URL, "/api/artifacts/") == id {
						return true
					}
				}
			}
		default:
			return false
		}
	}
	return false
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
	if !shareSnapshotReferencesAsset(share.Snapshot, "file", id) {
		writeError(w, 404, errNotFound)
		return
	}
	f, err := store.GetSharedFile(r.Context(), d.DB, id, share.ConversationID)
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
	if !shareSnapshotReferencesAsset(share.Snapshot, "artifact", id) {
		writeError(w, 404, errNotFound)
		return
	}
	a, err := store.GetSharedArtifact(r.Context(), d.DB, id, share.ConversationID)
	if err != nil || a == nil {
		writeError(w, 404, errNotFound)
		return
	}
	serveStoredArtifact(d, w, a)
}
