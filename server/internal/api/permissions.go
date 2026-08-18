package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

var errPermissionDenied = errors.New("permission denied")

const (
	permissionEpochKey                   = "permissions:epoch"
	globalCapabilityRevocationTopic      = "permissions:global-capabilities-revoked"
	groupPermissionRevocationTopicPrefix = "permissions:group:"
	permissionGenerationEpochPrefix      = "permissions:generation-epoch:"
)

type requestPermissionSnapshot struct {
	Permissions    store.UserGroupPermissions
	UserID         string
	GroupID        string
	GroupExpiresAt int64
	Epoch          string
	GlobalEpoch    string
	GroupEpoch     string
	UserEpoch      string
}

type globalCapabilitySnapshot struct {
	disabledTools map[string]struct{}
	memoryEnabled bool
}

// currentGlobalCapabilitySnapshot mirrors the runtime interpretation of the
// two platform-wide capability switches. Tool order and duplicates are not
// meaningful, and malformed/missing legacy values retain the same permissive
// defaults used by the catalog and generation paths.
func currentGlobalCapabilitySnapshot(d Deps) globalCapabilitySnapshot {
	snapshot := globalCapabilitySnapshot{
		disabledTools: map[string]struct{}{},
		memoryEnabled: true,
	}
	if d.DB == nil {
		return snapshot
	}
	if raw, err := store.GetSetting(d.DB, "disabled_tools"); err == nil {
		if names, _, parseErr := store.ParseBuiltinTools(raw); parseErr == nil {
			for _, name := range names {
				snapshot.disabledTools[name] = struct{}{}
			}
		}
	}
	snapshot.memoryEnabled = store.MemoryEnabledGlobal(d.DB)
	return snapshot
}

func globalCapabilitySnapshotsEqual(a, b globalCapabilitySnapshot) bool {
	if a.memoryEnabled != b.memoryEnabled || len(a.disabledTools) != len(b.disabledTools) {
		return false
	}
	for name := range a.disabledTools {
		if _, ok := b.disabledTools[name]; !ok {
			return false
		}
	}
	return true
}

func currentPermissionEpoch(d Deps) string {
	if d.Cache == nil {
		return "0"
	}
	if epoch, ok := d.Cache.Get(permissionEpochKey); ok {
		return epoch
	}
	return "0"
}

func bumpPermissionEpoch(d Deps) {
	if d.Cache != nil {
		d.Cache.Incr(permissionEpochKey, 0)
	}
}

func groupPermissionRevocationTopic(groupID string) string {
	return groupPermissionRevocationTopicPrefix + strings.TrimSpace(groupID)
}

func userPermissionRevocationTopic(userID string) string {
	return "user:" + strings.TrimSpace(userID) + ":kill"
}

func permissionGenerationEpochKey(scope, id string) string {
	key := permissionGenerationEpochPrefix + strings.TrimSpace(scope)
	if id = strings.TrimSpace(id); id != "" {
		key += ":" + id
	}
	return key
}

func permissionGenerationEpoch(d Deps, scope, id string) string {
	if d.Cache == nil {
		return "0"
	}
	if epoch, ok := d.Cache.Get(permissionGenerationEpochKey(scope, id)); ok {
		return epoch
	}
	return "0"
}

func bumpPermissionGenerationEpoch(d Deps, scope, id string) {
	if d.Cache != nil {
		d.Cache.Incr(permissionGenerationEpochKey(scope, id), 0)
	}
}

func revokeGroupPermissionSnapshots(d Deps, groupID string) {
	groupID = strings.TrimSpace(groupID)
	if d.Cache == nil || groupID == "" {
		return
	}
	bumpPermissionEpoch(d)
	bumpPermissionGenerationEpoch(d, "group", groupID)
	d.Cache.Publish(groupPermissionRevocationTopic(groupID), "1")
}

func revokeUserPermissionSnapshots(d Deps, userID string) {
	userID = strings.TrimSpace(userID)
	if d.Cache == nil || userID == "" {
		return
	}
	bumpPermissionEpoch(d)
	bumpPermissionGenerationEpoch(d, "user", userID)
	d.Cache.Publish(userPermissionRevocationTopic(userID), "permissions_updated")
}

func revokeGlobalCapabilitySnapshots(d Deps) {
	if d.Cache == nil {
		return
	}
	bumpPermissionEpoch(d)
	bumpPermissionGenerationEpoch(d, "global", "")
	d.Cache.Publish(globalCapabilityRevocationTopic, "1")
}

func requestPermissionSnapshotFor(d Deps, r *http.Request) (requestPermissionSnapshot, error) {
	u := authUser(r)
	if u == nil {
		return requestPermissionSnapshot{}, errPermissionDenied
	}
	if u.Role == "admin" {
		return requestPermissionSnapshot{
			Permissions: store.DefaultUserGroupPermissions(),
			UserID:      u.ID,
			Epoch:       currentPermissionEpoch(d),
			GlobalEpoch: permissionGenerationEpoch(d, "global", ""),
			UserEpoch:   permissionGenerationEpoch(d, "user", u.ID),
		}, nil
	}
	// A permission writer commits the database change before bumping the shared
	// epoch. Repeating on an epoch change yields a policy and version from one
	// stable interval; the generation watcher checks the epoch once more after it
	// subscribes, closing the remaining read-to-subscribe race.
	for attempts := 0; attempts < 3; attempts++ {
		before := currentPermissionEpoch(d)
		state, err := store.UserGroupPermissionStateForUser(r.Context(), d.DB, u.ID)
		if err != nil {
			return requestPermissionSnapshot{}, err
		}
		after := currentPermissionEpoch(d)
		if before == after {
			return requestPermissionSnapshot{
				Permissions:    state.Permissions,
				UserID:         u.ID,
				GroupID:        state.GroupID,
				GroupExpiresAt: state.GroupExpiresAt,
				Epoch:          after,
				GlobalEpoch:    permissionGenerationEpoch(d, "global", ""),
				GroupEpoch:     permissionGenerationEpoch(d, "group", state.GroupID),
				UserEpoch:      permissionGenerationEpoch(d, "user", u.ID),
			}, nil
		}
	}
	return requestPermissionSnapshot{}, errPermissionDenied
}

// requestPermissions always resolves the current group from the database. The
// auth snapshot is intentionally not trusted for authorization because group
// edits must take effect without waiting for a session refresh.
func requestPermissions(d Deps, r *http.Request) (store.UserGroupPermissions, error) {
	snapshot, err := requestPermissionSnapshotFor(d, r)
	return snapshot.Permissions, err
}

// catalogPermissions keeps catalog handlers independently testable while the
// real HTTP routes remain protected by requireAuth. Once a user is present,
// authorization always comes from the current database row.
func catalogPermissions(d Deps, r *http.Request) (store.UserGroupPermissions, error) {
	if authUser(r) == nil {
		return store.DefaultUserGroupPermissions(), nil
	}
	return requestPermissions(d, r)
}

func requireUserCapability(d Deps, w http.ResponseWriter, r *http.Request, allowed func(store.UserGroupPermissions) bool) bool {
	return requireUserCapabilityError(d, w, r, errPermissionDenied, allowed)
}

func requireUserCapabilityError(
	d Deps,
	w http.ResponseWriter,
	r *http.Request,
	denied error,
	allowed func(store.UserGroupPermissions) bool,
) bool {
	permissions, err := requestPermissions(d, r)
	if err != nil {
		writeError(w, http.StatusForbidden, denied)
		return false
	}
	if !allowed(permissions) {
		writeError(w, http.StatusForbidden, denied)
		return false
	}
	return true
}

func requireKnowledgeBasePermission(d Deps, w http.ResponseWriter, r *http.Request) bool {
	return requireUserCapabilityError(d, w, r, errKnowledgeBaseGroupPermission, func(p store.UserGroupPermissions) bool {
		return p.AllowKnowledgeBases
	})
}

func currentKnowledgeBasePermission(d Deps, r *http.Request) error {
	permissions, err := requestPermissions(d, r)
	if err != nil || !permissions.AllowKnowledgeBases {
		return errKnowledgeBaseGroupPermission
	}
	return nil
}

func currentFileUploadPermission(d Deps, r *http.Request) error {
	permissions, err := requestPermissions(d, r)
	if err != nil || !permissions.AllowFileUpload {
		return errFileUploadGroupPermission
	}
	return nil
}

func requireCapabilityHandler(
	denied error,
	allowed func(store.UserGroupPermissions) bool,
	next handler,
) handler {
	return func(d Deps, w http.ResponseWriter, r *http.Request) {
		if !requireUserCapabilityError(d, w, r, denied, allowed) {
			return
		}
		next(d, w, r)
	}
}

func requireKnowledgeBaseHandler(next handler) handler {
	return requireCapabilityHandler(errKnowledgeBaseGroupPermission, func(p store.UserGroupPermissions) bool {
		return p.AllowKnowledgeBases
	}, next)
}

func requireMemoryHandler(next handler) handler {
	return func(d Deps, w http.ResponseWriter, r *http.Request) {
		// The administrator setting is the instance-wide master switch. Keep it at
		// the HTTP boundary too so stale clients cannot manage memories after the
		// capability disappears from the UI and tool catalog.
		if !store.MemoryEnabledGlobal(d.DB) {
			writeError(w, http.StatusForbidden, errMemoryGroupPermission)
			return
		}
		if !requireUserCapabilityError(d, w, r, errMemoryGroupPermission, func(p store.UserGroupPermissions) bool {
			return p.AllowMemory
		}) {
			return
		}
		next(d, w, r)
	}
}

func turnUsesKnowledgeBase(conv *store.Conversation, raw json.RawMessage) bool {
	if conv == nil {
		return false
	}
	if strings.TrimSpace(conv.ProjectID) != "" {
		return true
	}
	selected := []string{}
	if len(raw) > 0 {
		if json.Unmarshal(raw, &selected) != nil {
			return false
		}
	} else if len(conv.KBIDs) > 0 {
		_ = json.Unmarshal(conv.KBIDs, &selected)
	}
	for _, id := range selected {
		if strings.TrimSpace(id) != "" {
			return true
		}
	}
	return false
}

func resolvePermittedUserSkillSelection(
	ctx context.Context,
	db *sql.DB,
	userID string,
	workspaceID string,
	ids []string,
	strict bool,
	policy store.ResourceAccessPolicy,
) ([]store.UserSkill, []string, error) {
	skills, normalized, err := store.ResolveUserSkillSelectionScoped(ctx, db, userID, workspaceID, ids, strict)
	if err != nil {
		return nil, nil, err
	}
	for _, skill := range skills {
		if !store.UserSkillPolicyAllows(policy, skill) {
			return nil, nil, errSkillGroupPermission
		}
	}
	return skills, normalized, nil
}

func applyTurnToolPermissions(
	permissions store.UserGroupPermissions,
	ids []string,
	configured bool,
) ([]string, bool) {
	if !configured {
		return ids, false
	}
	filtered := make([]string, 0, len(ids))
	for _, id := range ids {
		if toolPolicyAllowsID(permissions, id) {
			filtered = append(filtered, id)
		}
	}
	return filtered, true
}

func runToolAccessPolicy(permissions store.UserGroupPermissions) *llm.ToolAccessPolicy {
	return &llm.ToolAccessPolicy{
		Mode:         permissions.Tools.Mode,
		IDs:          append([]string(nil), permissions.Tools.IDs...),
		AllowDrawing: permissions.AllowDrawing,
		AllowMemory:  permissions.AllowMemory,
		AllowSkills:  skillPolicyHasResources(permissions.Skills),
		SkillMode:    permissions.Skills.Mode,
		SkillIDs:     append([]string(nil), permissions.Skills.IDs...),
	}
}

func skillPolicyHasResources(policy store.ResourceAccessPolicy) bool {
	return policy.Mode == store.ResourceAccessAll ||
		(policy.Mode == store.ResourceAccessSelected && len(policy.IDs) > 0)
}

func toolPolicyAllowsID(permissions store.UserGroupPermissions, id string) bool {
	if !store.ResourcePolicyAllows(permissions.Tools, id) {
		return false
	}
	if !permissions.AllowDrawing && (id == "builtin:image_generate" || id == "hosted:image_generation") {
		return false
	}
	if !permissions.AllowMemory && id == "builtin:save_memory" {
		return false
	}
	if !skillPolicyHasResources(permissions.Skills) && id == "builtin:use_skill" {
		return false
	}
	return true
}
