package api

import (
	"errors"
	"net/http"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

// defaultMemoryConfidence is the confidence stored for a user-created memory;
// overridable via env, defaults to the original 0.95.
var defaultMemoryConfidence = envcfg.F64("AIVORY_API_CONFIDENCE", 0.95)

// listMemoriesHandler returns the user's memories.
func listMemoriesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	status := r.URL.Query().Get("status")
	rows, err := store.ListMemories(r.Context(), d.DB, u.ID, status)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

type createMemoryReq struct {
	MemoryText string `json:"memory_text"`
	Slot       string `json:"slot"`
	Value      string `json:"value"`
}

// createMemoryHandler inserts a new ACTIVE memory.
func createMemoryHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req createMemoryReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if req.MemoryText == "" {
		writeError(w, 400, errors.New("memory_text required"))
		return
	}
	m, err := store.CreateMemory(r.Context(), d.DB, store.Memory{
		UserID:     u.ID,
		MemoryText: req.MemoryText,
		Slot:       req.Slot,
		Value:      req.Value,
		Status:     "ACTIVE",
		Confidence: defaultMemoryConfidence,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, m)
}

type updateMemoryReq struct {
	MemoryText string `json:"memory_text"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
}

// updateMemoryHandler edits the user-visible fields.
func updateMemoryHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	var req updateMemoryReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	m, err := store.UpdateMemoryText(r.Context(), d.DB, id, u.ID, req.MemoryText, req.Status, req.Reason)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 200, m)
}

// deleteMemoryHandler removes a memory.
func deleteMemoryHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	if err := store.DeleteMemory(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
