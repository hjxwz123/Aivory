package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aivory/server/internal/store"
)

func domainPersonalDataStatusHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	status, err := store.GetDomainPersonalDataStatus(r.Context(), d.DB, authUser(r).ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func dismissDomainPersonalDataHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	status, err := store.GetDomainPersonalDataStatus(r.Context(), d.DB, authUser(r).ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !status.NeedsAction {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err := store.DismissDomainPersonalDataPrompt(r.Context(), d.DB, authUser(r).ID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func migrateDomainPersonalDataHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	count, err := store.MigrateDomainPersonalConversations(r.Context(), d.DB, authUser(r).ID)
	if err != nil {
		if errors.Is(err, store.ErrForbidden) {
			writeError(w, http.StatusForbidden, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	publishUserEvent(d, r, authUser(r).ID, "conversation.updated", "")
	writeJSON(w, http.StatusOK, map[string]int{"migrated_conversations": count})
}

func requireDomainPersonalData(d Deps, w http.ResponseWriter, r *http.Request) bool {
	status, err := store.GetDomainPersonalDataStatus(r.Context(), d.DB, authUser(r).ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return false
	}
	if !status.NeedsAction {
		writeError(w, http.StatusNotFound, errNotFound)
		return false
	}
	return true
}

func listDomainPersonalConversationsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if !requireDomainPersonalData(d, w, r) {
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	rows, err := store.ListDomainPersonalConversations(r.Context(), d.DB, authUser(r).ID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for i := range rows {
		stripServerConvFields(&rows[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversations": rows,
		"limit":         limit,
		"offset":        offset,
		"has_more":      len(rows) == limit,
	})
}

func listDomainPersonalConversationMessagesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if !requireDomainPersonalData(d, w, r) {
		return
	}
	conversationID := pathParam(r, "id")
	conversation, err := store.GetConversation(r.Context(), d.DB, conversationID, authUser(r).ID)
	if err != nil || conversation.WorkspaceID != "" {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	messages, err := store.ListAllMessages(r.Context(), d.DB, conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, userMessageResponse(d, r, messages))
}
