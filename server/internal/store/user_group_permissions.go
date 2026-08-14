package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const (
	ResourceAccessAll      = "all"
	ResourceAccessSelected = "selected"
	ResourceAccessNone     = "none"
)

var ErrInvalidUserGroupPermissions = errors.New("invalid user group permissions")

// ResourceAccessPolicy controls access to an administrator-managed catalog.
// In selected mode only IDs is allowed. All and none intentionally ignore IDs.
type ResourceAccessPolicy struct {
	Mode string   `json:"mode"`
	IDs  []string `json:"ids"`
}

// UserGroupPermissions is the authoritative feature policy for a membership
// tier. Admin users bypass it at request boundaries, while regular users inherit
// it from their current group.
type UserGroupPermissions struct {
	Prompts                   ResourceAccessPolicy `json:"prompts"`
	Skills                    ResourceAccessPolicy `json:"skills"`
	Tools                     ResourceAccessPolicy `json:"tools"`
	AllowSharing              bool                 `json:"allow_sharing"`
	AllowKnowledgeBases       bool                 `json:"allow_knowledge_bases"`
	AllowKnowledgeBaseSharing bool                 `json:"allow_knowledge_base_sharing"`
	AllowFileUpload           bool                 `json:"allow_file_upload"`
	AllowConversationExport   bool                 `json:"allow_conversation_export"`
	AllowVoiceTranscription   bool                 `json:"allow_voice_transcription"`
	AllowMemory               bool                 `json:"allow_memory"`
	AllowDrawing              bool                 `json:"allow_drawing"`
}

// UserGroupPermissionState couples the policy with the membership row that
// selected it. Generation handlers use GroupID to subscribe to group-scoped
// revocations and GroupExpiresAt to stop a turn when a temporary tier expires.
type UserGroupPermissionState struct {
	Permissions    UserGroupPermissions
	GroupID        string
	GroupExpiresAt int64
}

func DefaultUserGroupPermissions() UserGroupPermissions {
	return UserGroupPermissions{
		Prompts:                   ResourceAccessPolicy{Mode: ResourceAccessAll, IDs: []string{}},
		Skills:                    ResourceAccessPolicy{Mode: ResourceAccessAll, IDs: []string{}},
		Tools:                     ResourceAccessPolicy{Mode: ResourceAccessAll, IDs: []string{}},
		AllowSharing:              true,
		AllowKnowledgeBases:       true,
		AllowKnowledgeBaseSharing: true,
		AllowFileUpload:           true,
		AllowConversationExport:   true,
		AllowVoiceTranscription:   true,
		AllowMemory:               true,
		AllowDrawing:              true,
	}
}

func normalizeResourceAccess(policy ResourceAccessPolicy) (ResourceAccessPolicy, error) {
	policy.Mode = strings.ToLower(strings.TrimSpace(policy.Mode))
	if policy.Mode == "" {
		policy.Mode = ResourceAccessAll
	}
	if policy.Mode != ResourceAccessAll && policy.Mode != ResourceAccessSelected && policy.Mode != ResourceAccessNone {
		return ResourceAccessPolicy{}, ErrInvalidUserGroupPermissions
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(policy.IDs))
	if policy.Mode == ResourceAccessSelected {
		for _, raw := range policy.IDs {
			id := strings.TrimSpace(raw)
			if id == "" || len(id) > 200 || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	policy.IDs = ids
	return policy, nil
}

// NormalizeUserGroupPermissions applies permissive defaults to missing legacy
// fields while preserving explicit false booleans in a present policy object.
func NormalizeUserGroupPermissions(raw json.RawMessage) (UserGroupPermissions, error) {
	defaults := DefaultUserGroupPermissions()
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "{}" || strings.TrimSpace(string(raw)) == "null" {
		return defaults, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return UserGroupPermissions{}, ErrInvalidUserGroupPermissions
	}
	permissions := defaults
	decode := func(key string, target any) error {
		value, ok := fields[key]
		if !ok {
			return nil
		}
		return json.Unmarshal(value, target)
	}
	if err := decode("prompts", &permissions.Prompts); err != nil {
		return UserGroupPermissions{}, ErrInvalidUserGroupPermissions
	}
	if err := decode("skills", &permissions.Skills); err != nil {
		return UserGroupPermissions{}, ErrInvalidUserGroupPermissions
	}
	if err := decode("tools", &permissions.Tools); err != nil {
		return UserGroupPermissions{}, ErrInvalidUserGroupPermissions
	}
	for key, target := range map[string]*bool{
		"allow_sharing":                &permissions.AllowSharing,
		"allow_knowledge_bases":        &permissions.AllowKnowledgeBases,
		"allow_knowledge_base_sharing": &permissions.AllowKnowledgeBaseSharing,
		"allow_file_upload":            &permissions.AllowFileUpload,
		"allow_conversation_export":    &permissions.AllowConversationExport,
		"allow_voice_transcription":    &permissions.AllowVoiceTranscription,
		"allow_memory":                 &permissions.AllowMemory,
		"allow_drawing":                &permissions.AllowDrawing,
	} {
		if err := decode(key, target); err != nil {
			return UserGroupPermissions{}, ErrInvalidUserGroupPermissions
		}
	}
	var err error
	if permissions.Prompts, err = normalizeResourceAccess(permissions.Prompts); err != nil {
		return UserGroupPermissions{}, err
	}
	if permissions.Skills, err = normalizeResourceAccess(permissions.Skills); err != nil {
		return UserGroupPermissions{}, err
	}
	if permissions.Tools, err = normalizeResourceAccess(permissions.Tools); err != nil {
		return UserGroupPermissions{}, err
	}
	// Sharing a personal knowledge base is a sub-capability of using knowledge
	// bases. Collapse contradictory payloads from stale or direct clients so a
	// later re-enable cannot silently restore sharing access the administrator
	// believed they had removed.
	if !permissions.AllowKnowledgeBases {
		permissions.AllowKnowledgeBaseSharing = false
	}
	return permissions, nil
}

func permissionsJSON(permissions UserGroupPermissions) (string, error) {
	raw, err := json.Marshal(permissions)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func UserGroupPermissionsForUser(ctx context.Context, db *sql.DB, userID string) (UserGroupPermissions, error) {
	state, err := UserGroupPermissionStateForUser(ctx, db, userID)
	return state.Permissions, err
}

// UserGroupPermissionStateForUser resolves an expired temporary membership
// before reading its policy. Authorization callers therefore do not depend on a
// profile refresh or a cooperative frontend timer for a tier expiry to apply.
func UserGroupPermissionStateForUser(ctx context.Context, db *sql.DB, userID string) (UserGroupPermissionState, error) {
	user := User{ID: userID}
	err := db.QueryRowContext(ctx, `SELECT role, group_id, group_expires_at, previous_group_id FROM users WHERE id=?`, userID).
		Scan(&user.Role, &user.GroupID, &user.GroupExpiresAt, &user.PreviousGroupID)
	if errors.Is(err, sql.ErrNoRows) {
		return UserGroupPermissionState{Permissions: DefaultUserGroupPermissions()}, ErrNotFound
	}
	if err != nil {
		return UserGroupPermissionState{Permissions: DefaultUserGroupPermissions()}, err
	}
	maybeExpireGroup(ctx, db, &user)
	state := UserGroupPermissionState{
		Permissions:    DefaultUserGroupPermissions(),
		GroupID:        user.GroupID,
		GroupExpiresAt: user.GroupExpiresAt,
	}
	if user.Role == "admin" {
		return state, nil
	}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(permissions,'{}') FROM user_groups WHERE id=?`, user.GroupID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return state, nil
		}
		return state, err
	}
	permissions, err := NormalizeUserGroupPermissions(json.RawMessage(raw))
	if err != nil {
		return state, err
	}
	state.Permissions = permissions
	return state, nil
}

func ResourcePolicyAllows(policy ResourceAccessPolicy, id string) bool {
	switch policy.Mode {
	case ResourceAccessNone:
		return false
	case ResourceAccessSelected:
		for _, allowed := range policy.IDs {
			if allowed == id {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// UserGroupPermissionsEqual compares policies by their effective meaning.
// Catalog ordering and duplicate IDs do not change authorization.
func UserGroupPermissionsEqual(a, b UserGroupPermissions) bool {
	return resourceAccessPoliciesEqual(a.Prompts, b.Prompts) &&
		resourceAccessPoliciesEqual(a.Skills, b.Skills) &&
		resourceAccessPoliciesEqual(a.Tools, b.Tools) &&
		a.AllowSharing == b.AllowSharing &&
		a.AllowKnowledgeBases == b.AllowKnowledgeBases &&
		a.AllowKnowledgeBaseSharing == b.AllowKnowledgeBaseSharing &&
		a.AllowFileUpload == b.AllowFileUpload &&
		a.AllowConversationExport == b.AllowConversationExport &&
		a.AllowVoiceTranscription == b.AllowVoiceTranscription &&
		a.AllowMemory == b.AllowMemory &&
		a.AllowDrawing == b.AllowDrawing
}

func resourceAccessPoliciesEqual(a, b ResourceAccessPolicy) bool {
	if a.Mode != b.Mode {
		return false
	}
	if a.Mode != ResourceAccessSelected {
		return true
	}
	ids := make(map[string]struct{}, len(a.IDs))
	for _, id := range a.IDs {
		ids[id] = struct{}{}
	}
	if len(ids) != len(b.IDs) {
		return false
	}
	for _, id := range b.IDs {
		if _, ok := ids[id]; !ok {
			return false
		}
	}
	return true
}

// FilterResourceIDs intersects an optional client selection with the current
// group policy. When configured is false, the policy itself becomes the
// explicit selection so callers cannot accidentally restore "all resources"
// by omitting a field that older clients did not send.
func FilterResourceIDs(policy ResourceAccessPolicy, ids []string, configured bool) ([]string, bool) {
	switch policy.Mode {
	case ResourceAccessNone:
		return []string{}, true
	case ResourceAccessSelected:
		allowed := make(map[string]bool, len(policy.IDs))
		for _, id := range policy.IDs {
			allowed[id] = true
		}
		if !configured {
			return append([]string(nil), policy.IDs...), true
		}
		filtered := make([]string, 0, len(ids))
		for _, id := range ids {
			if allowed[id] {
				filtered = append(filtered, id)
			}
		}
		return filtered, true
	default:
		return ids, configured
	}
}

func UserSkillPolicyAllows(policy ResourceAccessPolicy, skill UserSkill) bool {
	if policy.Mode == ResourceAccessAll {
		return true
	}
	return skill.SourceSkillID != "" && ResourcePolicyAllows(policy, skill.SourceSkillID)
}

func UserPromptPolicyAllows(policy ResourceAccessPolicy, prompt UserPrompt) bool {
	if policy.Mode == ResourceAccessAll {
		return true
	}
	return prompt.SourcePromptID != "" && ResourcePolicyAllows(policy, prompt.SourcePromptID)
}
