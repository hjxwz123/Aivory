package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/store"
)

func parseToolLogFilter(r *http.Request) (store.ToolCallLogFilter, error) {
	q := r.URL.Query()
	f := store.ToolCallLogFilter{Search: strings.TrimSpace(q.Get("q")), Kind: q.Get("kind"), Status: q.Get("status"), UserID: strings.TrimSpace(q.Get("user")), ToolName: strings.TrimSpace(q.Get("tool"))}
	if len(f.Search) > 512 || len(f.UserID) > 160 || len(f.ToolName) > 160 {
		return f, errors.New("tool log filter too long")
	}
	if f.Kind != "" && f.Kind != "builtin" && f.Kind != "mcp" {
		return f, errors.New("invalid tool kind")
	}
	switch f.Status {
	case "", "success", "error", "partial", "timeout", "canceled":
	default:
		return f, errors.New("invalid tool status")
	}
	for key, dest := range map[string]*int64{"from": &f.From, "until": &f.Until} {
		if value := q.Get(key); value != "" {
			timestamp, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return f, errors.New("tool log time must use RFC3339")
			}
			*dest = timestamp.UnixMilli()
			if *dest <= 0 {
				return f, errors.New("invalid tool log time")
			}
		}
	}
	if f.From > 0 && f.Until > 0 && f.From > f.Until {
		return f, errors.New("invalid tool log time range")
	}
	return f, nil
}

func adminToolLogsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	filter, err := parseToolLogFilter(r)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 || page > 1000000 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	logs, total, err := store.ListToolCallLogs(r.Context(), d.DB, filter, pageSize, (page-1)*pageSize)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"logs": logs, "total": total, "page": page, "page_size": pageSize})
}

func adminToolLogDetailHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	log, err := store.GetToolCallLog(r.Context(), d.DB, pathParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, errNotFound)
		return
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, log)
}

func deleteAdminToolLogHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	err := store.DeleteToolCallLog(r.Context(), d.DB, pathParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, errNotFound)
		return
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if s := auditState(r); s != nil {
		s.metadata["deleted_count"] = 1
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func deleteFilteredAdminToolLogsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	filter, err := parseToolLogFilter(r)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	now := time.Now().UnixMilli()
	if filter.Until == 0 || filter.Until > now {
		filter.Until = now
	}
	deleted, err := store.DeleteFilteredToolCallLogs(r.Context(), d.DB, filter)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if s := auditState(r); s != nil {
		s.metadata["deleted_count"] = deleted
		s.metadata["filter"] = filter
	}
	writeJSON(w, 200, map[string]int64{"deleted": deleted})
}
