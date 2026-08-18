package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"aivory/server/internal/store"
)

const (
	libraryNameMaxBytes        = 160
	libraryDescriptionMaxBytes = 4 << 10
	libraryContentMaxBytes     = 64 << 10
	userSkillNameMaxBytes      = 64
)

var userSkillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type catalogSkill struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Description        string `json:"description,omitempty"`
	DisplayDescription string `json:"display_description,omitempty"`
	Icon               string `json:"icon"`
	Source             string `json:"source"`
	Added              bool   `json:"added"`
}

type catalogPrompt struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon"`
	Source      string `json:"source"`
	Added       bool   `json:"added"`
}

type userSkillPayload struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	Instructions *string `json:"instructions"`
	WorkspaceID  string  `json:"workspace_id"`
}

type userPromptPayload struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Content     *string `json:"content"`
	WorkspaceID string  `json:"workspace_id"`
}

func libraryWorkspaceID(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("workspace_id"))
}

func authorizeLibraryWorkspace(d Deps, w http.ResponseWriter, r *http.Request, workspaceID string, write bool) bool {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return true
	}
	workspace, err := store.GetWorkspaceForMember(r.Context(), d.DB, workspaceID, authUser(r).ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return false
	}
	if write && (workspace.Role == store.WorkspaceRoleGuest || !workspace.CanCreateSkillsPrompts) {
		writeError(w, http.StatusForbidden, errForbidden)
		return false
	}
	return true
}

type adminPromptCreatePayload struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Content     string `json:"content"`
	Enabled     *bool  `json:"enabled"`
	SortOrder   int    `json:"sort_order"`
}

var forbiddenPrivateSkillKeys = map[string]bool{
	"asset": true, "assets": true,
	"file": true, "files": true,
	"storagepath": true,
	"attachment":  true, "attachments": true,
}

func normalizedJSONKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.NewReplacer("_", "", "-", "", " ", "").Replace(key)
	return key
}

// decodePrivateLibraryPayload applies a stricter contract than decodeJSON.
// Unknown top-level keys are rejected, and forbidden attachment/path keys are
// searched recursively and case-insensitively so alternate casing or nesting
// cannot create an accidental future file-ingest surface.
func decodePrivateLibraryPayload(r *http.Request, dst any, allowed map[string]bool) error {
	var fields map[string]json.RawMessage
	if err := decodeJSON(r, &fields); err != nil || fields == nil {
		return errInvalidInput
	}
	for key, raw := range fields {
		normalized := normalizedJSONKey(key)
		if forbiddenPrivateSkillKeys[normalized] || !allowed[normalized] {
			return errors.New("private skills and prompts do not accept files, assets, paths, or unknown fields")
		}
		var nested any
		if err := json.Unmarshal(raw, &nested); err != nil || containsForbiddenPrivateSkillKey(nested) {
			return errors.New("private skills and prompts do not accept files, assets, or storage paths")
		}
	}
	body, err := json.Marshal(fields)
	if err != nil || json.Unmarshal(body, dst) != nil {
		return errInvalidInput
	}
	return nil
}

func containsForbiddenPrivateSkillKey(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if forbiddenPrivateSkillKeys[normalizedJSONKey(key)] || containsForbiddenPrivateSkillKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if containsForbiddenPrivateSkillKey(child) {
				return true
			}
		}
	}
	return false
}

func listLibraryCatalogHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	workspaceID := libraryWorkspaceID(r)
	if !authorizeLibraryWorkspace(d, w, r, workspaceID, false) {
		return
	}
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil {
		writeError(w, http.StatusForbidden, errPermissionDenied)
		return
	}
	skills, err := store.ListSkills(r.Context(), d.DB, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	prompts, err := store.ListPrompts(r.Context(), d.DB, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	addedSkills, addedPrompts, err := store.UserLibrarySourceSetsScoped(r.Context(), d.DB, authUser(r).ID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	safeSkills := make([]catalogSkill, 0, len(skills))
	for _, skill := range skills {
		if !store.ResourcePolicyAllows(permissions.Skills, skill.ID) {
			continue
		}
		displayDescription := strings.TrimSpace(skill.DisplayDescription)
		safeSkills = append(safeSkills, catalogSkill{
			ID: skill.ID, Name: skill.Name, Description: displayDescription,
			DisplayDescription: displayDescription, Icon: skill.Icon, Source: "admin", Added: addedSkills[skill.ID],
		})
	}
	safePrompts := make([]catalogPrompt, 0, len(prompts))
	for _, prompt := range prompts {
		if !store.ResourcePolicyAllows(permissions.Prompts, prompt.ID) {
			continue
		}
		safePrompts = append(safePrompts, catalogPrompt{
			ID: prompt.ID, Name: prompt.Name, Description: prompt.Description,
			Icon: prompt.Icon, Source: "admin", Added: addedPrompts[prompt.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": safeSkills, "prompts": safePrompts})
}

// ===== Admin prompts =====

func listPromptsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListPrompts(r.Context(), d.DB, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func createPromptAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body adminPromptCreatePayload
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	prompt := store.Prompt{
		Name: body.Name, Description: body.Description, Icon: body.Icon, Content: body.Content,
		Enabled: enabled, SortOrder: body.SortOrder,
	}
	normalizePrompt(&prompt)
	if err := validatePrompt(prompt); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	created, err := store.CreatePrompt(r.Context(), d.DB, prompt)
	if err != nil {
		if errors.Is(err, store.ErrPromptNameExists) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func reorderPromptsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if err := store.ReorderPrompts(r.Context(), d.DB, body.IDs); err != nil {
		if errors.Is(err, store.ErrInvalidReorder) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func updatePromptAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	current, err := store.GetPrompt(r.Context(), d.DB, id)
	if err != nil {
		writeLibraryStoreError(w, err)
		return
	}
	prompt := *current
	if err := decodeJSON(r, &prompt); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	prompt.ID = id
	normalizePrompt(&prompt)
	if err := validatePrompt(prompt); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := store.UpdatePrompt(r.Context(), d.DB, id, prompt)
	if err != nil {
		if errors.Is(err, store.ErrPromptNameExists) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeLibraryStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func deletePromptAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	if err := store.DeletePrompt(r.Context(), d.DB, pathParam(r, "id")); err != nil {
		writeLibraryStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ===== User skills =====

func listMySkillsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	workspaceID := libraryWorkspaceID(r)
	if !authorizeLibraryWorkspace(d, w, r, workspaceID, false) {
		return
	}
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil {
		writeError(w, http.StatusForbidden, errSkillGroupPermission)
		return
	}
	rows, err := store.ListUserSkillsScoped(r.Context(), d.DB, authUser(r).ID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	filtered := rows[:0]
	for _, skill := range rows {
		if store.UserSkillPolicyAllows(permissions.Skills, skill) {
			filtered = append(filtered, skill)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func createMySkillHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil || permissions.Skills.Mode != store.ResourceAccessAll {
		writeError(w, http.StatusForbidden, errSkillGroupPermission)
		return
	}
	var body userSkillPayload
	if err := decodePrivateLibraryPayload(r, &body, map[string]bool{"name": true, "description": true, "instructions": true, "workspaceid": true}); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if body.Name == nil || body.Description == nil || body.Instructions == nil {
		writeError(w, http.StatusBadRequest, errors.New("name, description, and instructions required"))
		return
	}
	body.WorkspaceID = strings.TrimSpace(body.WorkspaceID)
	if !authorizeLibraryWorkspace(d, w, r, body.WorkspaceID, true) {
		return
	}
	skill := store.UserSkill{
		UserID: authUser(r).ID, WorkspaceID: body.WorkspaceID,
		Name: *body.Name, Description: *body.Description, Instructions: *body.Instructions,
	}
	normalizeUserSkill(&skill)
	if err := validateUserSkill(skill); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	created, err := store.CreateUserSkill(r.Context(), d.DB, skill)
	if err != nil {
		writeUserLibraryMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func updateMySkillHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	ownerID := authUser(r).ID
	id := pathParam(r, "id")
	workspaceID := libraryWorkspaceID(r)
	if !authorizeLibraryWorkspace(d, w, r, workspaceID, true) {
		return
	}
	current, err := store.GetUserSkillScoped(r.Context(), d.DB, id, ownerID, workspaceID)
	if err != nil {
		writeLibraryStoreError(w, err)
		return
	}
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil || !store.UserSkillPolicyAllows(permissions.Skills, *current) {
		writeError(w, http.StatusForbidden, errSkillGroupPermission)
		return
	}
	if !current.CanManage {
		writeError(w, http.StatusForbidden, errForbidden)
		return
	}
	var body userSkillPayload
	if err := decodePrivateLibraryPayload(r, &body, map[string]bool{"name": true, "description": true, "instructions": true}); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	skill := *current
	if body.Name != nil {
		skill.Name = *body.Name
	}
	if body.Description != nil {
		skill.Description = *body.Description
	}
	if body.Instructions != nil {
		skill.Instructions = *body.Instructions
	}
	normalizeUserSkill(&skill)
	if err := validateUserSkill(skill); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := store.UpdateUserSkillScoped(r.Context(), d.DB, id, ownerID, workspaceID, skill)
	if err != nil {
		writeUserLibraryMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func deleteMySkillHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	ownerID := authUser(r).ID
	id := pathParam(r, "id")
	workspaceID := libraryWorkspaceID(r)
	if !authorizeLibraryWorkspace(d, w, r, workspaceID, true) {
		return
	}
	current, err := store.GetUserSkillScoped(r.Context(), d.DB, id, ownerID, workspaceID)
	if err != nil {
		writeLibraryStoreError(w, err)
		return
	}
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil || !store.UserSkillPolicyAllows(permissions.Skills, *current) {
		writeError(w, http.StatusForbidden, errSkillGroupPermission)
		return
	}
	if !current.CanManage {
		writeError(w, http.StatusForbidden, errForbidden)
		return
	}
	if err := store.DeleteUserSkillScoped(r.Context(), d.DB, id, ownerID, workspaceID); err != nil {
		writeLibraryStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func copySkillFromCatalogHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		SourceID    string `json:"source_id"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.SourceID) == "" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	body.WorkspaceID = strings.TrimSpace(body.WorkspaceID)
	if !authorizeLibraryWorkspace(d, w, r, body.WorkspaceID, true) {
		return
	}
	source, err := store.GetSkill(r.Context(), d.DB, strings.TrimSpace(body.SourceID))
	if errors.Is(err, store.ErrNotFound) || err == nil && !source.Enabled {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil || !store.ResourcePolicyAllows(permissions.Skills, source.ID) {
		writeError(w, http.StatusForbidden, errSkillGroupPermission)
		return
	}
	skill := store.UserSkill{
		UserID: authUser(r).ID, WorkspaceID: body.WorkspaceID,
		Name: privateSkillNameFromCatalog(source.Name, source.ID), Description: source.Description,
		Icon: source.Icon, Instructions: source.Instructions, SourceSkillID: source.ID,
	}
	normalizeUserSkill(&skill)
	if err := validateUserSkill(skill); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("catalog skill is not compatible with the private Agent Skill format"))
		return
	}
	created, err := store.CreateUserSkill(r.Context(), d.DB, skill)
	if err != nil {
		writeUserLibraryMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func privateSkillNameFromCatalog(name, sourceID string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastHyphen := false
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			b.WriteRune(char)
			lastHyphen = false
		} else if b.Len() > 0 && !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	normalized := strings.Trim(b.String(), "-")
	if normalized == "" {
		fallback := normalizedJSONKey(sourceID)
		if fallback == "" {
			fallback = "catalog"
		}
		normalized = "skill-" + fallback
	}
	if len(normalized) > userSkillNameMaxBytes {
		normalized = strings.TrimRight(normalized[:userSkillNameMaxBytes], "-")
	}
	return normalized
}

// ===== User prompts =====

func listMyPromptsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	workspaceID := libraryWorkspaceID(r)
	if !authorizeLibraryWorkspace(d, w, r, workspaceID, false) {
		return
	}
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil {
		writeError(w, http.StatusForbidden, errPromptGroupPermission)
		return
	}
	rows, err := store.ListUserPromptsScoped(r.Context(), d.DB, authUser(r).ID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	filtered := rows[:0]
	for _, prompt := range rows {
		if store.UserPromptPolicyAllows(permissions.Prompts, prompt) {
			filtered = append(filtered, prompt)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func createMyPromptHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil || permissions.Prompts.Mode != store.ResourceAccessAll {
		writeError(w, http.StatusForbidden, errPromptGroupPermission)
		return
	}
	var body userPromptPayload
	if err := decodePrivateLibraryPayload(r, &body, map[string]bool{"name": true, "description": true, "content": true, "workspaceid": true}); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if body.Name == nil || body.Description == nil || body.Content == nil {
		writeError(w, http.StatusBadRequest, errors.New("name, description, and content required"))
		return
	}
	body.WorkspaceID = strings.TrimSpace(body.WorkspaceID)
	if !authorizeLibraryWorkspace(d, w, r, body.WorkspaceID, true) {
		return
	}
	prompt := store.UserPrompt{
		UserID: authUser(r).ID, WorkspaceID: body.WorkspaceID,
		Name: *body.Name, Description: *body.Description, Content: *body.Content,
	}
	normalizeUserPrompt(&prompt)
	if err := validateUserPrompt(prompt); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	created, err := store.CreateUserPrompt(r.Context(), d.DB, prompt)
	if err != nil {
		writeUserLibraryMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func updateMyPromptHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	ownerID := authUser(r).ID
	id := pathParam(r, "id")
	workspaceID := libraryWorkspaceID(r)
	if !authorizeLibraryWorkspace(d, w, r, workspaceID, true) {
		return
	}
	current, err := store.GetUserPromptScoped(r.Context(), d.DB, id, ownerID, workspaceID)
	if err != nil {
		writeLibraryStoreError(w, err)
		return
	}
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil || !store.UserPromptPolicyAllows(permissions.Prompts, *current) {
		writeError(w, http.StatusForbidden, errPromptGroupPermission)
		return
	}
	if !current.CanManage {
		writeError(w, http.StatusForbidden, errForbidden)
		return
	}
	var body userPromptPayload
	if err := decodePrivateLibraryPayload(r, &body, map[string]bool{"name": true, "description": true, "content": true}); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	prompt := *current
	if body.Name != nil {
		prompt.Name = *body.Name
	}
	if body.Description != nil {
		prompt.Description = *body.Description
	}
	if body.Content != nil {
		prompt.Content = *body.Content
	}
	normalizeUserPrompt(&prompt)
	if err := validateUserPrompt(prompt); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := store.UpdateUserPromptScoped(r.Context(), d.DB, id, ownerID, workspaceID, prompt)
	if err != nil {
		writeUserLibraryMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func deleteMyPromptHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	ownerID := authUser(r).ID
	id := pathParam(r, "id")
	workspaceID := libraryWorkspaceID(r)
	if !authorizeLibraryWorkspace(d, w, r, workspaceID, true) {
		return
	}
	current, err := store.GetUserPromptScoped(r.Context(), d.DB, id, ownerID, workspaceID)
	if err != nil {
		writeLibraryStoreError(w, err)
		return
	}
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil || !store.UserPromptPolicyAllows(permissions.Prompts, *current) {
		writeError(w, http.StatusForbidden, errPromptGroupPermission)
		return
	}
	if !current.CanManage {
		writeError(w, http.StatusForbidden, errForbidden)
		return
	}
	if err := store.DeleteUserPromptScoped(r.Context(), d.DB, id, ownerID, workspaceID); err != nil {
		writeLibraryStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func copyPromptFromCatalogHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		SourceID    string `json:"source_id"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.SourceID) == "" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	body.WorkspaceID = strings.TrimSpace(body.WorkspaceID)
	if !authorizeLibraryWorkspace(d, w, r, body.WorkspaceID, true) {
		return
	}
	source, err := store.GetPrompt(r.Context(), d.DB, strings.TrimSpace(body.SourceID))
	if errors.Is(err, store.ErrNotFound) || err == nil && !source.Enabled {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil || !store.ResourcePolicyAllows(permissions.Prompts, source.ID) {
		writeError(w, http.StatusForbidden, errPromptGroupPermission)
		return
	}
	prompt := store.UserPrompt{
		UserID: authUser(r).ID, WorkspaceID: body.WorkspaceID,
		Name: source.Name, Description: source.Description,
		Content: source.Content, SourcePromptID: source.ID,
	}
	normalizeUserPrompt(&prompt)
	if err := validateUserPrompt(prompt); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("catalog prompt exceeds private library limits"))
		return
	}
	created, err := store.CreateUserPrompt(r.Context(), d.DB, prompt)
	if err != nil {
		writeUserLibraryMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func normalizePrompt(prompt *store.Prompt) {
	prompt.Name = strings.TrimSpace(prompt.Name)
	prompt.Description = strings.TrimSpace(prompt.Description)
	prompt.Icon = strings.TrimSpace(prompt.Icon)
	prompt.Content = strings.TrimSpace(prompt.Content)
}

func normalizeUserSkill(skill *store.UserSkill) {
	skill.Name = strings.TrimSpace(skill.Name)
	skill.Description = strings.TrimSpace(skill.Description)
	skill.Icon = strings.TrimSpace(skill.Icon)
	skill.Instructions = strings.TrimSpace(skill.Instructions)
}

func normalizeUserPrompt(prompt *store.UserPrompt) {
	prompt.Name = strings.TrimSpace(prompt.Name)
	prompt.Description = strings.TrimSpace(prompt.Description)
	prompt.Content = strings.TrimSpace(prompt.Content)
}

func validatePrompt(prompt store.Prompt) error {
	return validateLibraryEntry(prompt.Name, prompt.Description, prompt.Content, "content")
}

func validateUserSkill(skill store.UserSkill) error {
	if len(skill.Name) > userSkillNameMaxBytes || !userSkillNamePattern.MatchString(skill.Name) {
		return errors.New("skill name must be lowercase kebab-case and at most 64 characters")
	}
	return validateLibraryEntry(skill.Name, skill.Description, skill.Instructions, "instructions")
}

func validateUserPrompt(prompt store.UserPrompt) error {
	return validateLibraryEntry(prompt.Name, prompt.Description, prompt.Content, "content")
}

func validateLibraryEntry(name, description, content, contentLabel string) error {
	if name == "" || description == "" || content == "" {
		return errors.New("name, description, and " + contentLabel + " required")
	}
	if len(name) > libraryNameMaxBytes {
		return errors.New("name is too long")
	}
	if len(description) > libraryDescriptionMaxBytes {
		return errors.New("description is too long")
	}
	if len(content) > libraryContentMaxBytes {
		return errors.New(contentLabel + " is too long")
	}
	return nil
}

func writeUserLibraryMutationError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrUserSkillNameExists) || errors.Is(err, store.ErrUserPromptNameExists) || errors.Is(err, store.ErrCatalogItemAlreadyAdded) {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeLibraryStoreError(w, err)
}

func writeLibraryStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}
