package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"aivory/server/internal/store"
)

const (
	maxSandboxFolderFiles     = 300
	maxSandboxFolderBytes     = 200 << 20
	maxSandboxFolderFileBytes = 40 << 20
	maxSandboxFolderPaths     = 256 << 10
)

func sandboxUploadAvailabilityHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"available": sandboxConfigured(d) && sandboxFolderGroupAllowed(d, r),
	})
}

func sandboxFolderGroupAllowed(d Deps, r *http.Request) bool {
	permissions, err := requestPermissions(d, r)
	if err != nil || !permissions.AllowFileUpload || !toolPolicyAllowsID(permissions, "builtin:python_execute") {
		return false
	}
	_, disabled := currentGlobalCapabilitySnapshot(d).disabledTools["python_execute"]
	return !disabled
}

func sandboxFolderAccess(d Deps, r *http.Request, convID string) (int, error) {
	if !sandboxConfigured(d) {
		return http.StatusServiceUnavailable, errors.New("sandbox not configured")
	}
	if !sandboxFolderGroupAllowed(d, r) {
		return http.StatusForbidden, errors.New("folder upload not permitted")
	}
	conv, err := store.GetConversation(r.Context(), d.DB, convID, authUser(r).ID)
	if err != nil {
		return http.StatusNotFound, errors.New("conversation not found")
	}
	if conv.WorkspaceID == "" {
		return 0, nil
	}
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: conv.WorkspaceID, UserID: authUser(r).ID,
		Action: store.ActionSandboxUse, Resource: "conversation", ResourceID: convID,
	})
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if !decision.Allowed {
		return http.StatusForbidden, errors.New("sandbox use not permitted")
	}
	policy, err := store.GetWorkspacePolicy(r.Context(), d.DB, conv.WorkspaceID)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if !policy.AllowFileUpload || policy.ToolDeniedByPolicy("builtin:python_execute") {
		return http.StatusForbidden, errors.New("workspace folder upload disabled")
	}
	return 0, nil
}

func readSandboxFolderField(part *multipart.Part, limit int64) (string, error) {
	data, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil || int64(len(data)) > limit {
		return "", errors.New("invalid folder metadata")
	}
	return string(data), nil
}

func uploadSandboxFolderHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	convID := pathParam(r, "id")
	if status, err := sandboxFolderAccess(d, r, convID); err != nil {
		writeError(w, status, err)
		return
	}
	if !rateLimitUser(d, authUser(r).ID, "upload", uploadRateLimitMax, uploadRateLimitWindow) {
		writeError(w, http.StatusTooManyRequests, errUploadRateLimited)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSandboxFolderBytes+maxSandboxFolderPaths+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	folderPart, err := reader.NextPart()
	if err != nil || folderPart.FormName() != "folder_name" {
		writeError(w, http.StatusBadRequest, errors.New("folder_name required"))
		return
	}
	folder, err := readSandboxFolderField(folderPart, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cleanFolder, ok := validateUploadRelPath(folder)
	if !ok || cleanFolder != folder || strings.Contains(folder, "/") {
		writeError(w, http.StatusBadRequest, errors.New("invalid folder name"))
		return
	}
	pathsPart, err := reader.NextPart()
	if err != nil || pathsPart.FormName() != "paths" {
		writeError(w, http.StatusBadRequest, errors.New("paths required"))
		return
	}
	pathsRaw, err := readSandboxFolderField(pathsPart, maxSandboxFolderPaths)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var paths []string
	if json.Unmarshal([]byte(pathsRaw), &paths) != nil || len(paths) == 0 || len(paths) > maxSandboxFolderFiles {
		writeError(w, http.StatusBadRequest, errors.New("invalid folder paths"))
		return
	}
	seen := make(map[string]bool, len(paths))
	for index, raw := range paths {
		cleaned, valid := validateUploadRelPath(raw)
		if !valid || cleaned != raw || !strings.HasPrefix(cleaned, folder+"/") || seen[cleaned] {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid file path at index %d", index))
			return
		}
		seen[cleaned] = true
	}
	if d.Tools == nil || d.Tools.Sandbox() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("sandbox not configured"))
		return
	}
	sessionID, err := d.Tools.EnsureConversationSandbox(r.Context(), convID, authUser(r).ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, errors.New("sandbox unavailable"))
		return
	}
	var total int64
	for index, relPath := range paths {
		part, err := reader.NextPart()
		if err != nil || part.FormName() != "file" || part.FileName() != path.Base(relPath) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("file does not match path at index %d", index))
			return
		}
		data, err := io.ReadAll(io.LimitReader(part, maxSandboxFolderFileBytes+1))
		if err != nil || len(data) > maxSandboxFolderFileBytes || total+int64(len(data)) > maxSandboxFolderBytes {
			writeError(w, http.StatusRequestEntityTooLarge, errors.New("folder exceeds upload limit"))
			return
		}
		total += int64(len(data))
		if status, err := sandboxFolderAccess(d, r, convID); err != nil {
			writeError(w, status, err)
			return
		}
		if err := d.Tools.Sandbox().PutFile(r.Context(), sessionID, "/workspace/folders/"+relPath, data); err != nil {
			writeError(w, http.StatusBadGateway, errors.New("sandbox folder upload failed"))
			return
		}
	}
	if part, err := reader.NextPart(); err != io.EOF || part != nil {
		writeError(w, http.StatusBadRequest, errors.New("unexpected folder data"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"folder": folder, "files": len(paths), "bytes": total})
}
