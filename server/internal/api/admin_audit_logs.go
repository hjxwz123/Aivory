package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/store"
)

func parseAuditFilter(r *http.Request) (store.AdminAuditFilter, error) {
	q := r.URL.Query()
	f := store.AdminAuditFilter{Search: strings.TrimSpace(q.Get("q")), Type: q.Get("type"), Result: q.Get("result"), Actor: q.Get("actor"), Target: q.Get("target"), Action: q.Get("action")}
	if len(f.Search) > 512 || len(f.Actor) > 160 || len(f.Target) > 160 || len(f.Action) > 160 {
		return f, errors.New("audit filter too long")
	}
	if f.Type != "" && !strings.Contains("|authentication|workspace|users|models|channels|billing|settings|access|integrations|content|logs|system|other|", "|"+f.Type+"|") {
		return f, errors.New("invalid audit type")
	}
	if f.Result != "" && f.Result != "success" && f.Result != "failure" && f.Result != "denied" && f.Result != "pending" {
		return f, errors.New("invalid audit result")
	}
	for key, dest := range map[string]*int64{"from": &f.From, "until": &f.Until} {
		if value := q.Get(key); value != "" {
			t, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return f, errors.New("audit time must use RFC3339")
			}
			*dest = t.UnixMilli()
		}
	}
	if f.From > 0 && f.Until > 0 && f.From > f.Until {
		return f, errors.New("invalid audit time range")
	}
	return f, nil
}

func adminAuditLogsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	filter, err := parseAuditFilter(r)
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
	logs, total, err := store.ListFilteredAdminAuditLogs(
		r.Context(), d.DB, filter, pageSize, (page-1)*pageSize,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"logs": logs, "total": total, "page": page, "page_size": pageSize,
	})
}

func exportAdminAuditLogsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	filter, err := parseAuditFilter(r)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	logs, total, err := store.ListFilteredAdminAuditLogs(r.Context(), d.DB, filter, 5000, 0)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if s := auditState(r); s != nil {
		s.metadata["exported_count"] = len(logs)
		s.metadata["matched_count"] = total
		s.metadata["truncated"] = total > len(logs)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="audit-logs.json"`)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"logs": logs, "total": total, "exported": len(logs), "truncated": total > len(logs), "exported_at": time.Now().UTC().Format(time.RFC3339)})
}

func deleteAdminAuditLogHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if err := store.DeleteAdminAuditLog(r.Context(), d.DB, pathParam(r, "id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	if s := auditState(r); s != nil {
		s.metadata["deleted_count"] = 1
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func deleteFilteredAdminAuditLogsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	filter, err := parseAuditFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Exclude events created after the confirmed deletion scope.
	now := time.Now().UnixMilli()
	if filter.Until == 0 || filter.Until > now {
		filter.Until = now
	}
	deleted, err := store.DeleteFilteredAdminAuditLogs(r.Context(), d.DB, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s := auditState(r); s != nil {
		s.metadata["deleted_count"] = deleted
		s.metadata["filter"] = filter
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": deleted})
}
