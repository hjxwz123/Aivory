package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/generationcfg"
	"aivory/server/internal/genstream"
	"aivory/server/internal/llm"
	"aivory/server/internal/msgcache"
	"aivory/server/internal/sse"
	"aivory/server/internal/store"
)

// SSE heartbeat and stream-replay tunables (env-overridable; defaults preserve
// prior hardcoded behavior).
var (
	ssePingHeartbeatPost        = envcfg.Dur("AIVORY_API_SSE_PING_HEARTBEAT_POST", 15*time.Second)
	ssePingHeartbeatRegenerate  = envcfg.Dur("AIVORY_API_SSE_PING_HEARTBEAT_REGENERATE", 15*time.Second)
	ssePingHeartbeatStream      = envcfg.Dur("AIVORY_API_SSE_PING_HEARTBEAT_STREAM", 15*time.Second)
	streamStatusRecheckInterval = envcfg.Dur("AIVORY_API_STREAM_STATUS_RECHECK_INTERVAL", 5*time.Second)
	streamReplayBatchSize       = envcfg.Int("AIVORY_API_STREAM_REPLAY_BATCH_SIZE", 200)
)

const chatRunErrorMessage = "The message could not be processed. Please try again."

func chatRunErrorEvent(err error, messageID string) llm.SseEvent {
	switch {
	case errors.Is(err, store.ErrStorageQuotaExceeded):
		return llm.SseEvent{
			Type:      "error",
			Message:   "Storage is full. Free up space in Files and try again.",
			MessageID: messageID,
			Code:      store.ErrStorageQuotaExceeded.Error(),
		}
	case errors.Is(err, llm.ErrDrawingPermission):
		return llm.SseEvent{
			Type:      "error",
			Message:   "Drawing is not available for this user group.",
			MessageID: messageID,
			Code:      errDrawingGroupPermission.Error(),
		}
	case errors.Is(err, llm.ErrKnowledgeBasePermission):
		return llm.SseEvent{
			Type:      "error",
			Message:   "Knowledge bases are not available for this user group.",
			MessageID: messageID,
			Code:      errKnowledgeBaseGroupPermission.Error(),
		}
	case llm.IsToolBudgetExceeded(err):
		return llm.SseEvent{
			Type:      "error",
			Message:   llm.ToolBudgetExceededMessage(),
			MessageID: messageID,
			Code:      "tool_budget_exceeded",
		}
	case llm.IsToolNoProgress(err):
		return llm.SseEvent{
			Type:      "error",
			Message:   llm.ToolNoProgressMessage(),
			MessageID: messageID,
			Code:      "tool_no_progress",
		}
	}
	return llm.SseEvent{Type: "error", Message: chatRunErrorMessage, MessageID: messageID}
}

type chatRunErrorMetadata struct {
	Operation       string
	UserID          string
	ConversationID  string
	Fast            bool
	Branch          bool
	ParentID        string
	ReferenceID     string
	AttachmentCount int
}

// logChatRunError records only request identifiers and turn-shape metadata.
// User text, attachment names/content, and assembled prompts must never be
// added here; the underlying error is enough for server-side diagnosis.
func logChatRunError(logger *log.Logger, meta chatRunErrorMetadata, err error) {
	if logger == nil || err == nil {
		return
	}
	logger.Printf(
		"chat run error: operation=%q user_id=%q conversation_id=%q fast=%t branch=%t parent_id=%q reference_id=%q attachment_count=%d error=%q",
		meta.Operation,
		meta.UserID,
		meta.ConversationID,
		meta.Fast,
		meta.Branch,
		meta.ParentID,
		meta.ReferenceID,
		meta.AttachmentCount,
		err.Error(),
	)
}

type postMessageReq struct {
	Text         string `json:"text"`
	ModelID      string `json:"model_id"`
	ParentID     string `json:"parent_id"`
	Branch       bool   `json:"branch"`
	Mode         string `json:"mode"`
	GenerationID string `json:"generation_id"`
	// KBIDs is a per-turn snapshot of the composer's current selection. Raw JSON
	// preserves the important difference between an omitted field (legacy client:
	// use the conversation setting) and [] (explicitly use no optional KBs).
	KBIDs json.RawMessage `json:"kb_ids"`
	// Verify enables Verify mode (§verify) — a secondary auditor model checks the
	// answer. No-op unless an admin configured `verify_model_id`.
	Verify bool `json:"verify"`
	// ToolMode is the per-turn tool policy: auto asks the configured task model,
	// disabled exposes no tools, and enabled exposes the complete administrator-
	// configured collection. RawMessage distinguishes an omitted
	// legacy request from explicitly invalid values such as null, an empty string,
	// or a non-string. NoTools
	// remains a backwards-compatible alias; an explicit ToolMode always wins.
	ToolMode json.RawMessage `json:"tool_mode"`
	NoTools  bool            `json:"no_tools"`
	// SelectedUserSkillIDs applies up to five private, user-owned Agent Skills to
	// this turn. The handler resolves ownership before opening SSE; the
	// orchestrator re-validates and persists the normalized ids.
	SelectedUserSkillIDs []string `json:"selected_user_skill_ids"`
	// SelectedToolIDs is omitted for the backwards-compatible all-tools policy;
	// an explicit [] is a real deny-all candidate subset.
	SelectedToolIDs json.RawMessage `json:"selected_tool_ids"`
	// WebSearch forces a server-side non-tool web search and is only meaningful
	// when tools are explicitly disabled.
	WebSearch bool `json:"web_search"`
	// Fast marks a fast-mode turn (§fast-mode): the model is resolved server-side
	// from the admin's fast model and masked from the user; Verify / Deep Research
	// / no-tools are all forced off; local system tools use independent fast-mode
	// budgets while provider-hosted tools keep their configured declarations.
	// Overrides ModelID (which the client omits on a fast turn).
	Fast           bool             `json:"fast"`
	Attachments    []llm.Attachment `json:"attachments"`
	ParamOverrides map[string]any   `json:"params"`
	// ImageStyleID selects an admin image style for an image-mode turn (§4.20).
	ImageStyleID string `json:"image_style_id"`
	// OptimizeImagePrompt defaults to true when omitted for compatibility with
	// older clients. False skips the task-model rewrite and sends the user's text
	// directly, while still applying an explicitly selected style directive.
	OptimizeImagePrompt *bool `json:"optimize_image_prompt"`
	// Locale is the user's current UI language (i18next code, e.g. "en", "zh");
	// drives the reply-language instruction (§ reply language).
	Locale string `json:"locale"`
}

const maxGenerationIDLength = 128

func validGenerationID(id string) bool {
	if id == "" || len(id) > maxGenerationIDLength {
		return false
	}
	for _, char := range id {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func generationStopTopic(userID, conversationID, generationID string) string {
	return "user:" + userID + ":conv:" + conversationID + ":generation:" + generationID + ":stop"
}

func conversationStopTopic(userID, conversationID string) string {
	return "user:" + userID + ":conv:" + conversationID + ":stop"
}

func workspaceGenerationRevocationTopic(workspaceID string) string {
	return "workspace:" + workspaceID + ":generation-revoked"
}

func workspaceGenerationRevocationKey(workspaceID string) string {
	return "workspace-generation-revoked:" + workspaceID
}

func workspaceMemberGenerationRevocationTopic(workspaceID, userID string) string {
	return "workspace:" + strings.TrimSpace(workspaceID) + ":user:" + strings.TrimSpace(userID) + ":generation-revoked"
}

func workspacePolicyGenerationRevocationTopic(workspaceID string) string {
	return "workspace:" + strings.TrimSpace(workspaceID) + ":policy:generation-revoked"
}

func conversationGenerationRevocationTopic(conversationID string) string {
	return "conversation:" + strings.TrimSpace(conversationID) + ":generation-revoked"
}

func knowledgeBaseGenerationRevocationTopic(kbID string) string {
	return "knowledge-base:" + strings.TrimSpace(kbID) + ":generation-revoked"
}

func knowledgeBaseUserGenerationRevocationTopic(kbID, userID string) string {
	return "knowledge-base:" + strings.TrimSpace(kbID) + ":user:" + strings.TrimSpace(userID) + ":generation-revoked"
}

func knowledgeBaseGenerationRevocationKey(kbID string) string {
	return "knowledge-base-generation-revoked:" + strings.TrimSpace(kbID)
}

func knowledgeBaseUserAccessEpochKey(kbID, userID string) string {
	return "knowledge-base-access-epoch:" + strings.TrimSpace(kbID) + ":" + strings.TrimSpace(userID)
}

func knowledgeBaseUserGenerationRevocationKey(kbID, userID, epoch string) string {
	return "knowledge-base-user-generation-revoked:" + strings.TrimSpace(kbID) + ":" + strings.TrimSpace(userID) + ":" + epoch
}

func currentKnowledgeBaseUserAccessEpoch(d Deps, kbID, userID string) string {
	if d.Cache == nil {
		return "0"
	}
	if epoch, ok := d.Cache.Get(knowledgeBaseUserAccessEpochKey(kbID, userID)); ok {
		return epoch
	}
	return "0"
}

// revokeKnowledgeBaseUserGenerations advances the access generation without
// poisoning a future re-share. Active turns retain their old epoch in the
// stream deny-list, while newly authorized turns capture the incremented one.
func revokeKnowledgeBaseUserGenerations(d Deps, kbID, userID string) {
	kbID = strings.TrimSpace(kbID)
	userID = strings.TrimSpace(userID)
	if d.Cache == nil || kbID == "" || userID == "" {
		return
	}
	epochKey := knowledgeBaseUserAccessEpochKey(kbID, userID)
	epoch := currentKnowledgeBaseUserAccessEpoch(d, kbID, userID)
	d.Cache.Set(knowledgeBaseUserGenerationRevocationKey(kbID, userID, epoch), "1", genstream.RevocationTTL())
	d.Cache.Incr(epochKey, 0)
	d.Cache.Publish(knowledgeBaseUserGenerationRevocationTopic(kbID, userID), "1")
}

func revokeKnowledgeBaseGenerations(d Deps, kbID string) {
	kbID = strings.TrimSpace(kbID)
	if d.Cache == nil || kbID == "" {
		return
	}
	d.Cache.Set(knowledgeBaseGenerationRevocationKey(kbID), "1", genstream.RevocationTTL())
	d.Cache.Publish(knowledgeBaseGenerationRevocationTopic(kbID), "1")
}

type generationKnowledgeBaseAccessSnapshot struct {
	UserID         string
	ConversationID string
	WorkspaceID    string
	IDs            []string
	UserEpochs     map[string]string
}

func captureGenerationKnowledgeBaseAccess(
	d Deps, userID, conversationID, workspaceID string, ids []string,
) *generationKnowledgeBaseAccessSnapshot {
	if len(ids) == 0 {
		return nil
	}
	snapshot := &generationKnowledgeBaseAccessSnapshot{
		UserID: userID, ConversationID: conversationID, WorkspaceID: workspaceID,
		IDs: append([]string(nil), ids...), UserEpochs: make(map[string]string, len(ids)),
	}
	for _, kbID := range snapshot.IDs {
		snapshot.UserEpochs[kbID] = currentKnowledgeBaseUserAccessEpoch(d, kbID, userID)
	}
	return snapshot
}

func generationKnowledgeBaseDenyKeys(snapshot *generationKnowledgeBaseAccessSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	keys := make([]string, 0, len(snapshot.IDs)*2)
	for _, kbID := range snapshot.IDs {
		keys = append(keys, knowledgeBaseGenerationRevocationKey(kbID))
		keys = append(keys, knowledgeBaseUserGenerationRevocationKey(kbID, snapshot.UserID, snapshot.UserEpochs[kbID]))
	}
	return keys
}

func generationStreamDenyKeys(workspaceID string) []string {
	if strings.TrimSpace(workspaceID) == "" {
		return nil
	}
	return []string{workspaceGenerationRevocationKey(workspaceID)}
}

func generationStreamDenyKeysForAccess(
	workspaceID string, knowledgeBases *generationKnowledgeBaseAccessSnapshot,
) []string {
	keys := generationStreamDenyKeys(workspaceID)
	return append(keys, generationKnowledgeBaseDenyKeys(knowledgeBases)...)
}

func revokeMessageGenerationStreams(d Deps, messageIDs []string) error {
	failed := false
	for _, messageID := range messageIDs {
		if strings.TrimSpace(messageID) == "" {
			continue
		}
		if !genstream.Revoke(d.Cache, messageID) {
			failed = true
		}
		// Publish even when the atomic cache mutation failed. Live subscribers can
		// still stop immediately, while the handler reports failure instead of
		// falsely acknowledging a revocation whose replay tombstone is uncertain.
		d.Cache.Publish(genstream.RevocationTopic(messageID), "1")
	}
	if failed {
		return errors.New("generation stream revocation unavailable")
	}
	return nil
}

func publishWorkspaceGenerationRevocation(d Deps, workspaceID string) bool {
	if strings.TrimSpace(workspaceID) == "" {
		return false
	}
	key := workspaceGenerationRevocationKey(workspaceID)
	d.Cache.Set(key, "1", genstream.RevocationTTL())
	if _, stored := d.Cache.Get(key); !stored {
		return false
	}
	d.Cache.Publish(workspaceGenerationRevocationTopic(workspaceID), "1")
	return true
}

// revokeWorkspaceMemberGenerations stops only the target member's active
// turns. It is intentionally an ephemeral broadcast rather than a permanent
// deny key: a later promotion must be able to start a new turn immediately.
func revokeWorkspaceMemberGenerations(d Deps, workspaceID, userID string) {
	if d.Cache == nil || strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(userID) == "" {
		return
	}
	d.Cache.Publish(workspaceMemberGenerationRevocationTopic(workspaceID, userID), "1")
}

// revokeWorkspacePolicyGenerations invalidates currently running work after
// any policy mutation. New turns load the committed policy afresh.
func revokeWorkspacePolicyGenerations(d Deps, workspaceID string) {
	if d.Cache == nil || strings.TrimSpace(workspaceID) == "" {
		return
	}
	d.Cache.Publish(workspacePolicyGenerationRevocationTopic(workspaceID), "1")
}

// revokeConversationGenerations closes a visibility-change race. It cancels
// all active turns on that conversation; current authorization determines who
// may start a replacement turn.
func revokeConversationGenerations(d Deps, conversationID string) {
	if d.Cache == nil || strings.TrimSpace(conversationID) == "" {
		return
	}
	d.Cache.Publish(conversationGenerationRevocationTopic(conversationID), "1")
}

type generationWorkspaceAccessSnapshot struct {
	WorkspaceID    string
	ConversationID string
	UserID         string
	Policy         store.WorkspacePolicy
}

// captureGenerationWorkspaceAccess makes the request-start authorization
// explicit. GetConversation deliberately admits read-only guests, while a
// generation requires reply authority; checking here prevents a guest from
// getting as far as model setup or a provider call.
func captureGenerationWorkspaceAccess(
	ctx context.Context, db *sql.DB, conv *store.Conversation, userID string,
) (*generationWorkspaceAccessSnapshot, error) {
	if conv == nil || strings.TrimSpace(conv.WorkspaceID) == "" {
		return nil, nil
	}
	decision, err := store.AuthorizeWorkspace(ctx, db, store.WorkspaceAuthorizationRequest{
		WorkspaceID: conv.WorkspaceID,
		UserID:      userID,
		Action:      store.ActionConversationReply,
		Resource:    "conversation",
		ResourceID:  conv.ID,
	})
	if err != nil {
		return nil, err
	}
	if !decision.Allowed {
		return nil, errForbidden
	}
	policy, err := store.GetWorkspacePolicy(ctx, db, conv.WorkspaceID)
	if err != nil {
		return nil, err
	}
	return &generationWorkspaceAccessSnapshot{
		WorkspaceID: conv.WorkspaceID, ConversationID: conv.ID, UserID: userID, Policy: policy,
	}, nil
}

func (snapshot *generationWorkspaceAccessSnapshot) stillCurrent(ctx context.Context, db *sql.DB) bool {
	if snapshot == nil {
		return true
	}
	decision, err := store.AuthorizeWorkspace(ctx, db, store.WorkspaceAuthorizationRequest{
		WorkspaceID: snapshot.WorkspaceID,
		UserID:      snapshot.UserID,
		Action:      store.ActionConversationReply,
		Resource:    "conversation",
		ResourceID:  snapshot.ConversationID,
	})
	if err != nil || !decision.Allowed {
		return false
	}
	policy, err := store.GetWorkspacePolicy(ctx, db, snapshot.WorkspaceID)
	if err != nil {
		return false
	}
	return workspacePoliciesEqual(snapshot.Policy, policy)
}

func workspacePoliciesEqual(a, b store.WorkspacePolicy) bool {
	if a.WorkspaceID != b.WorkspaceID || a.AllowSandbox != b.AllowSandbox ||
		a.AllowImageGeneration != b.AllowImageGeneration ||
		a.AllowKnowledgeBases != b.AllowKnowledgeBases || a.AllowFileUpload != b.AllowFileUpload ||
		a.MemberMonthlyCreditLimit != b.MemberMonthlyCreditLimit {
		return false
	}
	return slices.Equal(a.AllowedModelIDs, b.AllowedModelIDs) &&
		slices.Equal(a.AllowedToolIDs, b.AllowedToolIDs) &&
		slices.Equal(a.AllowedMCPServerIDs, b.AllowedMCPServerIDs)
}

func subscribePermanentRevocation(
	d Deps, ctx context.Context, cancel context.CancelFunc, topic string, revoked func() bool,
) func() {
	if strings.TrimSpace(topic) == "" {
		return func() {}
	}
	ch, unsubscribe := d.Cache.Subscribe(topic)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					// Losing the shared revocation channel is security-significant
					// for a detached workspace generation, so fail closed.
					cancel()
					return
				}
				cancel()
				return
			case <-ticker.C:
				if revoked() {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	// Subscribe first, then inspect the permanent marker. A revoke on either side
	// of setup is observed by at least one of the channel or tombstone.
	if revoked() {
		cancel()
	}
	return unsubscribe
}

type generationAccessRevocationWatcher struct {
	d                     Deps
	ctx                   context.Context
	cancel                context.CancelFunc
	workspaceUnsub        func()
	workspaceAccessUnsubs []func()
	workspaceAccess       *generationWorkspaceAccessSnapshot
	workspaceRevoked      atomic.Bool
	permissionUnsubs      []func()
	knowledgeUnsubs       []func()
	knowledgeBases        *generationKnowledgeBaseAccessSnapshot
	knowledgeRevoked      atomic.Bool
	permissionRevoked     atomic.Bool
	permissions           *requestPermissionSnapshot
	conversationID        string
	userID                string
	messageMu             sync.Mutex
	messageID             string
	groupExpiryTimer      *time.Timer
	messageOnce           sync.Once
	messageUnsub          func()
}

func subscribeAccessRevocationTopic(
	d Deps, ctx context.Context, cancel context.CancelFunc, topic string,
) func() {
	if d.Cache == nil || strings.TrimSpace(topic) == "" {
		return func() {}
	}
	ch, unsubscribe := d.Cache.Subscribe(topic)
	var closing atomic.Bool
	go func() {
		select {
		case _, ok := <-ch:
			// A lost cross-replica subscription is security-significant for a
			// detached generation. Match the workspace watcher and fail closed,
			// except when this request is deliberately releasing its subscription.
			if !ok && !closing.Load() {
				cancel()
				return
			}
			if ok {
				cancel()
			}
		case <-ctx.Done():
		}
	}()
	return func() {
		closing.Store(true)
		unsubscribe()
	}
}

func newGenerationAccessRevocationWatcher(
	d Deps, ctx context.Context, cancel context.CancelFunc, conversationID, workspaceID string,
	permissions *requestPermissionSnapshot,
	knowledgeBases *generationKnowledgeBaseAccessSnapshot,
	workspaceAccess *generationWorkspaceAccessSnapshot,
) *generationAccessRevocationWatcher {
	watcher := &generationAccessRevocationWatcher{
		d: d, ctx: ctx, cancel: cancel, workspaceUnsub: func() {}, knowledgeBases: knowledgeBases,
		permissions: permissions, conversationID: strings.TrimSpace(conversationID), workspaceAccess: workspaceAccess,
	}
	if strings.TrimSpace(workspaceID) != "" {
		key := workspaceGenerationRevocationKey(workspaceID)
		watcher.workspaceUnsub = subscribePermanentRevocation(
			d, ctx, cancel, workspaceGenerationRevocationTopic(workspaceID),
			func() bool { _, revoked := d.Cache.Get(key); return revoked },
		)
	}
	if workspaceAccess != nil {
		watcher.workspaceAccessUnsubs = append(watcher.workspaceAccessUnsubs,
			subscribeAccessRevocationTopic(d, ctx, watcher.revokeWorkspaceAccess,
				workspaceMemberGenerationRevocationTopic(workspaceAccess.WorkspaceID, workspaceAccess.UserID)),
			subscribeAccessRevocationTopic(d, ctx, watcher.revokeWorkspaceAccess,
				workspacePolicyGenerationRevocationTopic(workspaceAccess.WorkspaceID)),
			subscribeAccessRevocationTopic(d, ctx, watcher.revokeWorkspaceAccess,
				conversationGenerationRevocationTopic(workspaceAccess.ConversationID)),
		)
		if !watcher.workspaceAccessStillCurrent() {
			watcher.revokeWorkspaceAccess()
		}
		// Pub/sub delivers prompt revocation across replicas. The periodic DB
		// recheck is the fail-closed backstop for a missed event or a direct SQL
		// administration change.
		go watcher.watchWorkspaceAccess()
	}
	if permissions != nil {
		watcher.userID = permissions.UserID
		watcher.permissionUnsubs = append(watcher.permissionUnsubs,
			subscribeAccessRevocationTopic(d, ctx, watcher.revokePermissionAccess, globalCapabilityRevocationTopic),
			subscribeAccessRevocationTopic(d, ctx, watcher.revokePermissionAccess, userPermissionRevocationTopic(permissions.UserID)),
		)
		if strings.TrimSpace(permissions.GroupID) != "" {
			watcher.permissionUnsubs = append(watcher.permissionUnsubs,
				subscribeAccessRevocationTopic(d, ctx, watcher.revokePermissionAccess, groupPermissionRevocationTopic(permissions.GroupID)),
			)
		}
		if permissions.GroupExpiresAt > 0 {
			untilExpiry := time.Until(time.Unix(permissions.GroupExpiresAt, 0))
			if untilExpiry <= 0 {
				watcher.revokePermissionAccess()
			} else {
				watcher.groupExpiryTimer = time.AfterFunc(untilExpiry, watcher.revokePermissionAccess)
			}
		}
		// Subscribe first, then compare the version captured with the database
		// policy. A revoke racing setup is observed by either its topic or this
		// version check.
		if !watcher.permissionAccessStillCurrent() {
			watcher.revokePermissionAccess()
		}
	}
	if knowledgeBases != nil {
		for _, kbID := range knowledgeBases.IDs {
			watcher.knowledgeUnsubs = append(watcher.knowledgeUnsubs,
				watcher.subscribeKnowledgeBaseRevocation(knowledgeBaseGenerationRevocationTopic(kbID)),
				watcher.subscribeKnowledgeBaseRevocation(knowledgeBaseUserGenerationRevocationTopic(kbID, knowledgeBases.UserID)),
			)
		}
		// Subscribe before checking the epochs and database. A revoke racing the
		// initial selection is observed either by its topic, the epoch comparison,
		// the permanent deny key, or this authoritative access recheck.
		if !watcher.knowledgeBaseAccessStillCurrent() {
			watcher.revokeKnowledgeBaseAccess()
		}
	}
	return watcher
}

func (watcher *generationAccessRevocationWatcher) workspaceAccessStillCurrent() bool {
	return watcher == nil || watcher.workspaceAccess == nil || watcher.workspaceAccess.stillCurrent(watcher.ctx, watcher.d.DB)
}

func (watcher *generationAccessRevocationWatcher) watchWorkspaceAccess() {
	if watcher == nil || watcher.workspaceAccess == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-watcher.ctx.Done():
			return
		case <-ticker.C:
			if !watcher.workspaceAccessStillCurrent() {
				watcher.revokeWorkspaceAccess()
				return
			}
		}
	}
}

func (watcher *generationAccessRevocationWatcher) revokeWorkspaceAccess() {
	if watcher == nil {
		return
	}
	watcher.workspaceRevoked.Store(true)
	watcher.cancel()
	watcher.messageMu.Lock()
	messageID := watcher.messageID
	watcher.messageMu.Unlock()
	if messageID != "" {
		_ = genstream.Revoke(watcher.d.Cache, messageID)
		if watcher.d.Cache != nil {
			watcher.d.Cache.Publish(genstream.RevocationTopic(messageID), "1")
		}
		watcher.scrubRevokedMessage(messageID, "workspace")
	}
}

func (watcher *generationAccessRevocationWatcher) permissionAccessStillCurrent() bool {
	if watcher == nil || watcher.permissions == nil {
		return true
	}
	permissions := watcher.permissions
	if permissionGenerationEpoch(watcher.d, "global", "") != permissions.GlobalEpoch ||
		permissionGenerationEpoch(watcher.d, "user", permissions.UserID) != permissions.UserEpoch {
		return false
	}
	return strings.TrimSpace(permissions.GroupID) == "" ||
		permissionGenerationEpoch(watcher.d, "group", permissions.GroupID) == permissions.GroupEpoch
}

func (watcher *generationAccessRevocationWatcher) revokePermissionAccess() {
	if watcher == nil {
		return
	}
	watcher.permissionRevoked.Store(true)
	watcher.cancel()
	watcher.messageMu.Lock()
	messageID := watcher.messageID
	watcher.messageMu.Unlock()
	if messageID != "" {
		_ = genstream.Revoke(watcher.d.Cache, messageID)
		if watcher.d.Cache != nil {
			watcher.d.Cache.Publish(genstream.RevocationTopic(messageID), "1")
		}
		watcher.scrubRevokedMessage(messageID, "permission")
	}
}

func (watcher *generationAccessRevocationWatcher) subscribeKnowledgeBaseRevocation(topic string) func() {
	if watcher == nil || watcher.d.Cache == nil || strings.TrimSpace(topic) == "" {
		return func() {}
	}
	ch, unsubscribe := watcher.d.Cache.Subscribe(topic)
	var closing atomic.Bool
	go func() {
		select {
		case _, ok := <-ch:
			if ok || !closing.Load() {
				watcher.revokeKnowledgeBaseAccess()
			}
		case <-watcher.ctx.Done():
		}
	}()
	return func() {
		closing.Store(true)
		unsubscribe()
	}
}

func (watcher *generationAccessRevocationWatcher) knowledgeBaseAccessStillCurrent() bool {
	if watcher == nil || watcher.knowledgeBases == nil {
		return true
	}
	for _, kbID := range watcher.knowledgeBases.IDs {
		if currentKnowledgeBaseUserAccessEpoch(watcher.d, kbID, watcher.knowledgeBases.UserID) != watcher.knowledgeBases.UserEpochs[kbID] {
			return false
		}
		if watcher.d.Cache != nil {
			if _, revoked := watcher.d.Cache.Get(knowledgeBaseGenerationRevocationKey(kbID)); revoked {
				return false
			}
		}
		kb, err := store.GetKB(watcher.ctx, watcher.d.DB, kbID, watcher.knowledgeBases.UserID)
		if err != nil || kb.WorkspaceID != watcher.knowledgeBases.WorkspaceID {
			return false
		}
	}
	return true
}

func (watcher *generationAccessRevocationWatcher) revokeKnowledgeBaseAccess() {
	if watcher == nil {
		return
	}
	watcher.knowledgeRevoked.Store(true)
	watcher.cancel()
	watcher.messageMu.Lock()
	messageID := watcher.messageID
	watcher.messageMu.Unlock()
	if messageID != "" {
		_ = genstream.Revoke(watcher.d.Cache, messageID)
		if watcher.d.Cache != nil {
			watcher.d.Cache.Publish(genstream.RevocationTopic(messageID), "1")
		}
		watcher.scrubRevokedMessage(messageID, "knowledge-base")
	}
}

func (watcher *generationAccessRevocationWatcher) scrubRevokedMessage(messageID, cause string) {
	if watcher == nil || strings.TrimSpace(messageID) == "" || watcher.conversationID == "" || watcher.userID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	changed, err := store.ScrubAccessRevokedGeneration(
		ctx,
		watcher.d.DB,
		messageID,
		watcher.conversationID,
		watcher.userID,
	)
	if err != nil {
		if watcher.d.Logger != nil {
			watcher.d.Logger.Printf(
				"access-revoked generation scrub failed: cause=%q conversation_id=%q message_id=%q user_id=%q error=%q",
				cause,
				watcher.conversationID,
				messageID,
				watcher.userID,
				err.Error(),
			)
		}
		return
	}
	if changed {
		msgcache.Bump(watcher.d.Cache, watcher.conversationID)
	}
}

func (watcher *generationAccessRevocationWatcher) watchMessage(messageID string) {
	if watcher == nil || strings.TrimSpace(messageID) == "" {
		return
	}
	watcher.messageOnce.Do(func() {
		watcher.messageMu.Lock()
		watcher.messageID = messageID
		watcher.messageMu.Unlock()
		watcher.messageUnsub = subscribePermanentRevocation(
			watcher.d, watcher.ctx, watcher.cancel, genstream.RevocationTopic(messageID),
			func() bool { return genstream.IsRevoked(watcher.d.Cache, messageID) },
		)
		if watcher.permissionRevoked.Load() {
			_ = genstream.Revoke(watcher.d.Cache, messageID)
			if watcher.d.Cache != nil {
				watcher.d.Cache.Publish(genstream.RevocationTopic(messageID), "1")
			}
			watcher.scrubRevokedMessage(messageID, "permission")
		}
		if watcher.knowledgeRevoked.Load() {
			_ = genstream.Revoke(watcher.d.Cache, messageID)
			if watcher.d.Cache != nil {
				watcher.d.Cache.Publish(genstream.RevocationTopic(messageID), "1")
			}
			watcher.scrubRevokedMessage(messageID, "knowledge-base")
		}
		if watcher.workspaceRevoked.Load() {
			_ = genstream.Revoke(watcher.d.Cache, messageID)
			if watcher.d.Cache != nil {
				watcher.d.Cache.Publish(genstream.RevocationTopic(messageID), "1")
			}
			watcher.scrubRevokedMessage(messageID, "workspace")
		}
	})
}

func (watcher *generationAccessRevocationWatcher) close() {
	if watcher == nil {
		return
	}
	if watcher.messageUnsub != nil {
		watcher.messageUnsub()
	}
	if watcher.groupExpiryTimer != nil {
		watcher.groupExpiryTimer.Stop()
	}
	for _, unsubscribe := range watcher.permissionUnsubs {
		unsubscribe()
	}
	for _, unsubscribe := range watcher.workspaceAccessUnsubs {
		unsubscribe()
	}
	for _, unsubscribe := range watcher.knowledgeUnsubs {
		unsubscribe()
	}
	if watcher.workspaceUnsub != nil {
		watcher.workspaceUnsub()
	}
}

func messageStopTopic(userID, conversationID, messageID string) string {
	return "user:" + userID + ":conv:" + conversationID + ":message:" + messageID + ":stop"
}

func scopedStopIntentKey(topic string) string {
	return "stop-intent:" + topic
}

func scopedStopIntentTTL() time.Duration {
	return generationcfg.ProtectedDuration()
}

func publishScopedStop(d Deps, topic string) {
	// Pub/Sub is intentionally best-effort. The short-lived marker closes the
	// race where Stop reaches the server before the generation has subscribed.
	d.Cache.Set(scopedStopIntentKey(topic), "1", scopedStopIntentTTL())
	d.Cache.Publish(topic, "1")
}

func subscribeScopedStop(d Deps, ctx context.Context, onStop func(), topic string) func() {
	ch, unsubscribe := d.Cache.Subscribe(topic)
	go func() {
		select {
		case _, ok := <-ch:
			if ok {
				onStop()
			}
		case <-ctx.Done():
		}
	}()
	// Subscribe first, then inspect the durable marker. A stop arriving on either
	// side of this check is therefore observed by the marker or the channel.
	if _, stopped := d.Cache.Get(scopedStopIntentKey(topic)); stopped {
		onStop()
	}
	return func() {
		unsubscribe()
		d.Cache.Delete(scopedStopIntentKey(topic))
	}
}

type generationStopWatcher struct {
	d               Deps
	ctx             context.Context
	cancel          context.CancelFunc
	userID          string
	conversationID  string
	generationUnsub func()
	messageOnce     sync.Once
	messageUnsub    func()
	stopRequested   atomic.Bool
	active          atomic.Bool
}

func newGenerationStopWatcher(
	d Deps,
	ctx context.Context,
	cancel context.CancelFunc,
	userID, conversationID, generationID string,
) *generationStopWatcher {
	watcher := &generationStopWatcher{
		d: d, ctx: ctx, cancel: cancel, userID: userID, conversationID: conversationID,
	}
	if generationID != "" {
		watcher.generationUnsub = subscribeScopedStop(
			d,
			ctx,
			watcher.requestStop,
			generationStopTopic(userID, conversationID, generationID),
		)
	}
	return watcher
}

func (watcher *generationStopWatcher) requestStop() {
	if watcher == nil {
		return
	}
	watcher.stopRequested.Store(true)
	if watcher.active.Load() {
		watcher.cancel()
	}
}

// activate applies a stop only after the assistant placeholder exists and its
// message-scoped watcher is installed. Before that point a generation marker is
// remembered but must not cancel database setup, or the user row/assistant row
// can be left missing or half-created when Stop wins the subscription race.
func (watcher *generationStopWatcher) activate() {
	if watcher == nil {
		return
	}
	watcher.active.Store(true)
	if watcher.stopRequested.Load() {
		watcher.cancel()
	}
}

func (watcher *generationStopWatcher) watchMessage(messageID string) {
	if watcher == nil || messageID == "" {
		return
	}
	watcher.messageOnce.Do(func() {
		watcher.messageUnsub = subscribeScopedStop(
			watcher.d,
			watcher.ctx,
			watcher.requestStop,
			messageStopTopic(watcher.userID, watcher.conversationID, messageID),
		)
	})
}

func (watcher *generationStopWatcher) close() {
	if watcher == nil {
		return
	}
	if watcher.generationUnsub != nil {
		watcher.generationUnsub()
	}
	if watcher.messageUnsub != nil {
		watcher.messageUnsub()
	}
}

func validTurnToolMode(mode string) bool {
	switch mode {
	case llm.ToolModeAuto, llm.ToolModeDisabled, llm.ToolModeEnabled:
		return true
	}
	return false
}

// resolveTurnToolMode maps the legacy no_tools boolean onto the three-state
// protocol. An explicit per-turn choice always wins; when it is omitted, an
// explicit legacy disable wins and otherwise the account/deployment default is
// used. Invalid explicit values are rejected instead of silently changing how
// a turn is executed.
func resolveTurnToolMode(explicit json.RawMessage, legacyNoTools bool, defaultMode string) (string, error) {
	if len(explicit) == 0 {
		if legacyNoTools {
			return llm.ToolModeDisabled, nil
		}
		if normalized, valid := normalizedDefaultToolMode(defaultMode); valid {
			return normalized, nil
		}
		return llm.ToolModeAuto, nil
	}
	var mode string
	if err := json.Unmarshal(explicit, &mode); err != nil {
		return "", errors.New("tool_mode must be one of: auto, disabled, enabled")
	}
	// Rolling-upgrade compatibility: the retired user-selectable hosted-only mode
	// now means the unified enabled collection. Any accompanying legacy selection
	// field is ignored by JSON decoding.
	if mode == llm.ToolModeOfficial {
		return llm.ToolModeEnabled, nil
	}
	if !validTurnToolMode(mode) {
		return "", errors.New("tool_mode must be one of: auto, disabled, enabled")
	}
	return mode, nil
}

func parseSelectedToolIDs(raw json.RawMessage) (ids []string, configured bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	var values []string
	if unmarshalErr := json.Unmarshal(raw, &values); unmarshalErr != nil || values == nil {
		return nil, true, errors.New("selected_tool_ids must be an array of tool ids")
	}
	if len(values) > 256 {
		return nil, true, errors.New("selected_tool_ids contains too many items")
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value)
		separator := strings.IndexByte(id, ':')
		if len(id) > 160 || separator <= 0 || separator == len(id)-1 {
			return nil, true, errors.New("selected_tool_ids contains an invalid tool id")
		}
		switch id[:separator] {
		case "builtin", "hosted", "mcp":
		default:
			return nil, true, errors.New("selected_tool_ids contains an invalid tool id")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, true, nil
}

// resolveTurnKnowledgeBaseSelection validates a request-scoped KB snapshot.
// The conversation PATCH remains the durable preference, but generation must
// not depend on whether that separate request happened to win a network race.
// Project libraries are deliberately excluded from the returned explicit ids;
// the orchestrator attaches them from conv.ProjectID on every project turn.
func resolveTurnKnowledgeBaseSelection(
	ctx context.Context,
	db *sql.DB,
	userID string,
	conv *store.Conversation,
	raw json.RawMessage,
) (ids []string, configured bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	var requested []string
	if json.Unmarshal(raw, &requested) != nil || requested == nil {
		return nil, true, fmt.Errorf("%w: kb_ids must be an array of knowledge-base ids", errInvalidInput)
	}
	if len(requested) > 64 {
		return nil, true, fmt.Errorf("%w: kb_ids contains too many items", errInvalidInput)
	}
	for _, id := range requested {
		if value := strings.TrimSpace(id); value == "" || len(value) > 160 {
			return nil, true, fmt.Errorf("%w: kb_ids contains an invalid knowledge-base id", errInvalidInput)
		}
	}
	ids, err = store.ResolveOwnedKBIDs(ctx, db, userID, conv.WorkspaceID, requested)
	if err != nil {
		return nil, true, err
	}
	compatibilityIDs := append([]string(nil), ids...)
	if conv.ProjectID != "" {
		project, projectErr := store.GetProject(ctx, db, conv.ProjectID, userID)
		if projectErr != nil {
			return nil, true, projectErr
		}
		if project.KBID != "" {
			compatibilityIDs = append(compatibilityIDs, project.KBID)
		}
	}
	if compatibilityErr := store.ValidateKBEmbeddingCompatibility(ctx, db, compatibilityIDs); compatibilityErr != nil {
		return nil, true, compatibilityErr
	}
	return ids, true, nil
}

func generationKnowledgeBaseIDs(
	ctx context.Context,
	db *sql.DB,
	userID string,
	conv *store.Conversation,
	turnIDs []string,
	turnSelectionConfigured bool,
) []string {
	ids := append([]string(nil), turnIDs...)
	if !turnSelectionConfigured && conv != nil && len(conv.KBIDs) > 0 {
		var persisted []string
		if json.Unmarshal(conv.KBIDs, &persisted) == nil {
			ids = store.OwnedKBIDs(ctx, db, userID, conv.WorkspaceID, persisted)
		}
	}
	if conv != nil && conv.ProjectID != "" {
		if project, err := store.GetProject(ctx, db, conv.ProjectID, userID); err == nil && project.KBID != "" {
			ids = append(ids, project.KBID)
		}
	}
	seen := make(map[string]bool, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	return normalized
}

// normalizeTurnFlags enforces feature mutual exclusion server-side. Deep
// Research always needs tools; forced web search is the explicit-disabled
// fallback and cannot be combined with auto/enabled policies.
func normalizeTurnFlags(mode, toolMode string, webSearch bool) (string, bool) {
	if mode == "deep-research" {
		return llm.ToolModeEnabled, false
	}
	if toolMode != llm.ToolModeDisabled {
		return toolMode, false
	}
	return toolMode, webSearch
}

// postMessageHandler is the SSE-streaming endpoint. The orchestrator owns the
// full lifecycle; this handler simply opens the stream, runs the orchestrator
// and writes events to the wire.
func postMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	conv, err := store.GetConversation(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	var req postMessageReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	permissionSnapshot, err := requestPermissionSnapshotFor(d, r)
	if err != nil {
		writeError(w, http.StatusForbidden, errPermissionDenied)
		return
	}
	permissions := permissionSnapshot.Permissions
	if !permissions.AllowFileUpload && len(req.Attachments) > 0 {
		writeError(w, http.StatusForbidden, errFileUploadGroupPermission)
		return
	}
	if !permissions.AllowKnowledgeBases && turnUsesKnowledgeBase(conv, req.KBIDs) {
		writeError(w, http.StatusForbidden, errKnowledgeBaseGroupPermission)
		return
	}
	if req.GenerationID != "" && !validGenerationID(req.GenerationID) {
		writeError(w, 400, errors.New("invalid generation_id"))
		return
	}
	turnKBIDs, turnKBSelectionConfigured, err := resolveTurnKnowledgeBaseSelection(
		r.Context(), d.DB, u.ID, conv, req.KBIDs,
	)
	if err != nil {
		switch {
		case errors.Is(err, errInvalidInput):
			writeError(w, http.StatusBadRequest, err)
		case errors.Is(err, store.ErrMixedKBEmbeddingModels):
			writeError(w, http.StatusConflict, errKnowledgeBaseSelectionIncompatible)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	workspaceAccess, workspaceAccessErr := captureGenerationWorkspaceAccess(r.Context(), d.DB, conv, u.ID)
	if workspaceAccessErr != nil {
		if errors.Is(workspaceAccessErr, errForbidden) {
			writeError(w, http.StatusForbidden, errForbidden)
		} else {
			writeError(w, http.StatusInternalServerError, workspaceAccessErr)
		}
		return
	}
	knowledgeBaseAccess := captureGenerationKnowledgeBaseAccess(
		d, u.ID, id, conv.WorkspaceID,
		generationKnowledgeBaseIDs(r.Context(), d.DB, u.ID, conv, turnKBIDs, turnKBSelectionConfigured),
	)
	_, normalizedSkillIDs, err := resolvePermittedUserSkillSelection(
		r.Context(), d.DB, u.ID, conv.WorkspaceID, req.SelectedUserSkillIDs, true, permissions.Skills,
	)
	if err != nil {
		if errors.Is(err, errSkillGroupPermission) {
			writeError(w, http.StatusForbidden, errSkillGroupPermission)
		} else if errors.Is(err, store.ErrInvalidUserSkillSelection) {
			writeError(w, http.StatusBadRequest, err)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	req.SelectedUserSkillIDs = normalizedSkillIDs
	// A branch edit names the exact persisted message it forks from. Reject a
	// stale optimistic id (or a message from another conversation) before opening
	// the SSE response, so the client receives a clear conflict instead of a 200
	// stream that later contains a database foreign-key error.
	if req.Branch && req.ParentID != "" {
		parent, parentErr := store.GetMessage(r.Context(), d.DB, req.ParentID)
		if parentErr != nil && !errors.Is(parentErr, store.ErrNotFound) {
			writeError(w, 500, parentErr)
			return
		}
		if parentErr != nil || parent.ConversationID != id {
			writeError(w, http.StatusConflict, llm.ErrInvalidMessageParent)
			return
		}
	}
	// Resolve every client attachment id to the complete server-owned file row
	// before any readiness/capability check or persistence. This both enforces the
	// conversation access boundary and prevents forged kind/MIME/name/URL fields
	// from reaching the orchestrator and provider serializers.
	req.Attachments, err = normalizeConversationAttachments(r.Context(), d.DB, id, u.ID, req.Attachments)
	if err != nil {
		writeError(w, attachmentNormalizationErrorStatus(err), err)
		return
	}
	if err := validateTurnContent(r.Context(), d.DB, conv, req.ModelID, req.Fast, req.Text, req.Attachments); err != nil {
		status := imageCapabilityErrorStatus(err)
		if errors.Is(err, errMessageTextRequired) || errors.Is(err, errImagePromptRequired) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	if !permissions.AllowDrawing {
		model, modelErr := resolveEffectiveConversationModel(r.Context(), d.DB, conv, req.ModelID, req.Fast)
		if modelErr != nil {
			writeError(w, imageCapabilityErrorStatus(modelErr), modelErr)
			return
		}
		if model.Kind == "image" {
			writeError(w, http.StatusForbidden, errDrawingGroupPermission)
			return
		}
	}
	// §workspace RBAC phase 4: re-check the workspace capability policy for
	// this turn — model allowlist, image switch, attachments, knowledge bases
	// and the member monthly credit limit (fail closed on lookup errors). A
	// model-less request keeps the orchestrator's own error path.
	{
		effectiveModel, modelErr := resolveEffectiveConversationModel(r.Context(), d.DB, conv, req.ModelID, req.Fast)
		if modelErr != nil && !errors.Is(modelErr, errNoModelConfigured) {
			writeError(w, imageCapabilityErrorStatus(modelErr), modelErr)
			return
		}
		if err := enforceWorkspaceTurnPolicy(r.Context(), d.DB, conv, u.ID, effectiveModel,
			len(req.Attachments), permissions.AllowKnowledgeBases && turnUsesKnowledgeBase(conv, req.KBIDs)); err != nil {
			writeError(w, workspacePolicyErrorStatus(err), err)
			return
		}
	}
	if err := ensureAttachedDocumentsReadyForUser(r.Context(), d.DB, id, u.ID, req.Attachments); err != nil {
		writeError(w, 409, err)
		return
	}
	// Images remain durable conversation files even for text-only models. The
	// orchestrator/provider layer strips image blocks from the model request when
	// vision is unavailable, while python_execute can still stage the original
	// bytes from the conversation file row. Do not reject the turn before SSE.
	toolMode, err := resolveTurnToolMode(req.ToolMode, req.NoTools, effectiveDefaultToolMode(d.DB, u.Settings))
	if err != nil {
		writeError(w, 400, err)
		return
	}
	selectedToolIDs, selectedToolsConfigured, err := parseSelectedToolIDs(req.SelectedToolIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	selectedToolIDs, selectedToolsConfigured = applyTurnToolPermissions(permissions, selectedToolIDs, selectedToolsConfigured)
	if !toolPolicyAllowsID(permissions, "builtin:aivory_web_search") {
		req.WebSearch = false
	}
	// Deep Research is a per-group capability (§ user groups). If the user's
	// group isn't entitled, silently downgrade to a normal turn (the client also
	// hides the button, so this is defense-in-depth, not the primary UX).
	// §fast-mode overrides every other turn flag: a fast turn never runs Verify,
	// Deep Research, no-tools, or forced web search (the orchestrator re-enforces
	// this, but keep the request self-consistent here too).
	if req.Fast {
		req.Mode = ""
		req.Verify = false
		toolMode = llm.ToolModeEnabled
		req.WebSearch = false
	}
	if req.Mode == "deep-research" && u.Role != "admin" && !userGroupHasFeature(r.Context(), d, permissionSnapshot.GroupID, "research") {
		req.Mode = ""
	}
	toolMode, req.WebSearch = normalizeTurnFlags(req.Mode, toolMode, req.WebSearch)
	// §8 hard rule: per-user concurrent generation cap. Reserve the slot FIRST,
	// before the daily-message counter is incremented — otherwise a request that
	// is rejected here (slot full) would still burn a daily count for a turn that
	// never ran. Released when the SSE handler returns.
	release, ok := reserveConcurrentGen(d, u.ID)
	if !ok {
		writeError(w, 429, errors.New("too many concurrent generations — wait for the current one to finish or stop it"))
		return
	}
	defer release()
	// Admins are exempt from all usage quotas (§ admin) — they can test freely.
	if u.Role != "admin" {
		// Limit per day.
		if !checkDailyMessageLimit(d, u.ID) {
			writeError(w, 429, errors.New("daily message limit reached"))
			return
		}
		// §8 hard rule: daily token ceiling. 0 = disabled.
		if !checkDailyTokenQuota(d, u.ID) {
			writeError(w, 429, errors.New("daily token quota reached"))
			return
		}
	}

	writer := sse.New(w)
	if writer == nil {
		writeError(w, 500, errors.New("streaming not supported"))
		return
	}

	// Build the cancellable context: HTTP disconnect + per-conversation stop +
	// access revocation (including the user kill topic used by real-time bans).
	// The reply must survive the user closing the page mid-stream: detach the
	// generation from the HTTP request so a browser disconnect no longer aborts
	// (and loses) the answer — it finishes server-side and is persisted, ready
	// when the user returns. Only an explicit stop/kill or the hard time cap can
	// cancel it now.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), generationcfg.MaxDuration())
	defer cancel()
	accessRevocation := newGenerationAccessRevocationWatcher(
		d, ctx, cancel, id, conv.WorkspaceID, &permissionSnapshot, knowledgeBaseAccess, workspaceAccess,
	)
	defer accessRevocation.close()
	scopedStop := newGenerationStopWatcher(d, ctx, cancel, u.ID, id, req.GenerationID)
	defer scopedStop.close()
	stopCh, unsub := d.Cache.Subscribe(conversationStopTopic(u.ID, id))
	defer unsub()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Periodic ping.
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go func() {
		t := time.NewTicker(ssePingHeartbeatPost)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
				writer.Ping()
			}
		}
	}()

	streamMessageID := ""
	terminalSent := false
	sendEvent := func(ev llm.SseEvent) {
		if ev.Type == "message_start" && ev.MessageID != "" {
			streamMessageID = ev.MessageID
			// Install message-scoped cancellation before exposing the persisted id
			// to the browser. Capture it even after a concurrent revoke canceled the
			// context, so that revoke can still scrub the just-created placeholder.
			scopedStop.watchMessage(streamMessageID)
			scopedStop.activate()
			accessRevocation.watchMessage(streamMessageID)
		}
		// A provider may ignore cancellation briefly or return buffered deltas after
		// Stop/ban/workspace revocation. Never forward those late contents; only the
		// synthetic stopped terminal is valid once the turn context is canceled.
		if ctx.Err() != nil && ev.Type != "message_start" && (ev.Type != "done" || ev.StopReason != "stopped") {
			return
		}
		if streamMessageID != "" && ev.MessageID == "" {
			ev.MessageID = streamMessageID
		}
		if genstream.Terminal(ev) {
			terminalSent = true
		}
		if streamMessageID != "" {
			eventID, appended, revoked := genstream.Append(
				d.Cache, streamMessageID, ev,
				generationStreamDenyKeysForAccess(conv.WorkspaceID, knowledgeBaseAccess)...,
			)
			if revoked {
				cancel()
				if ev.Type == "done" && ev.StopReason == "stopped" {
					_ = writer.Send(ev, ev.Type)
				}
				return
			}
			if appended {
				_ = writer.SendID(ev, ev.Type, eventID)
				return
			}
			// Workspace streams depend on shared cache revocation. If Redis is
			// unavailable, direct-write fallback would bypass cross-replica kicks.
			if conv.WorkspaceID != "" {
				cancel()
				return
			}
		}
		if ctx.Err() != nil && ev.Type != "message_start" && (ev.Type != "done" || ev.StopReason != "stopped") {
			return
		}
		_ = writer.Send(ev, ev.Type)
	}

	runResult, runErr := d.Orchestrator.Run(ctx, llm.RunRequest{
		UserID:                  u.ID,
		ConversationID:          id,
		ModelID:                 req.ModelID,
		UserText:                req.Text,
		Attachments:             req.Attachments,
		ParentID:                req.ParentID,
		Branch:                  req.Branch,
		Mode:                    req.Mode,
		Verify:                  req.Verify,
		ToolMode:                toolMode,
		SelectedUserSkillIDs:    req.SelectedUserSkillIDs,
		SelectedToolIDs:         selectedToolIDs,
		SelectedToolsConfigured: selectedToolsConfigured,
		ToolAccessPolicy:        workspaceTurnToolPolicy(r.Context(), d.DB, conv, runToolAccessPolicy(permissions)),
		WorkspaceAccessCheck: func(checkCtx context.Context) error {
			if workspaceAccess.stillCurrent(checkCtx, d.DB) {
				return nil
			}
			return errors.New("workspace access revoked")
		},
		ForceWebSearch:                   req.WebSearch,
		Fast:                             req.Fast,
		ParamOverrides:                   req.ParamOverrides,
		ImageStyleID:                     req.ImageStyleID,
		OptimizeImagePrompt:              req.OptimizeImagePrompt,
		Locale:                           req.Locale,
		KnowledgeBaseIDs:                 turnKBIDs,
		KnowledgeBaseSelectionConfigured: turnKBSelectionConfigured,
	}, sendEvent)
	err = runErr
	if streamMessageID == "" && runResult != nil && runResult.AssistantMessage != nil {
		streamMessageID = runResult.AssistantMessage.ID
	}
	if !accessRevocation.permissionAccessStillCurrent() {
		accessRevocation.revokePermissionAccess()
	}
	if !accessRevocation.knowledgeBaseAccessStillCurrent() {
		accessRevocation.revokeKnowledgeBaseAccess()
	}
	if accessRevocation.permissionRevoked.Load() {
		accessRevocation.scrubRevokedMessage(streamMessageID, "permission")
	}
	if accessRevocation.knowledgeRevoked.Load() {
		accessRevocation.scrubRevokedMessage(streamMessageID, "knowledge-base")
	}
	if ctx.Err() != nil && !terminalSent {
		sendEvent(llm.SseEvent{Type: "done", Message: "", MessageID: streamMessageID, StopReason: "stopped"})
		publishUserEvent(d, r, u.ID, "conversation.updated", id)
		return
	}
	if err != nil && !terminalSent {
		parentID := req.ParentID
		if parentID == "" && !req.Branch {
			parentID = conv.ActiveLeafID
		}
		logChatRunError(d.Logger, chatRunErrorMetadata{
			Operation:       "post_message",
			UserID:          u.ID,
			ConversationID:  id,
			Fast:            req.Fast,
			Branch:          req.Branch,
			ParentID:        parentID,
			AttachmentCount: len(req.Attachments),
		}, err)
		sendEvent(chatRunErrorEvent(err, streamMessageID))
	}
	// §23: the turn is over (success, stop, or error — the user message and any
	// partial answer are persisted either way); nudge the user's other devices.
	publishUserEvent(d, r, u.ID, "conversation.updated", id)
}

func ensureAttachedDocumentsReady(ctx context.Context, db *sql.DB, convID string, atts []llm.Attachment) error {
	return ensureAttachedDocumentsReadyForUser(ctx, db, convID, "", atts)
}

// ensureAttachedDocumentsReadyForUser performs the same document readiness
// checks while limiting the unsent-draft invariant to the current uploader.
// In a shared conversation, another member's composer draft must not make this
// request fail or become an attachment candidate.
func ensureAttachedDocumentsReadyForUser(ctx context.Context, db *sql.DB, convID, userID string, atts []llm.Attachment) error {
	docIDs := []string{}
	fileIDs := []string{}
	seen := map[string]bool{}
	attachedFiles := map[string]bool{}
	queuedFiles := map[string]bool{}
	for _, a := range atts {
		if fileID := strings.TrimSpace(a.ID); fileID != "" {
			attachedFiles[fileID] = true
		}
		id := strings.TrimSpace(a.DocumentID)
		if id != "" {
			if seen[id] {
				continue
			}
			seen[id] = true
			docIDs = append(docIDs, id)
			continue
		}
		fileID := strings.TrimSpace(a.ID)
		if fileID == "" || queuedFiles[fileID] || !isDocKind(a.Kind) {
			continue
		}
		queuedFiles[fileID] = true
		fileIDs = append(fileIDs, fileID)
	}
	// Never trust the refreshed client to remember local attachment state. Every
	// server-side composer draft must be present in this turn; otherwise the user
	// could refresh while parsing, send with attachments=[], and receive an answer
	// that silently ignored the file.
	var drafts []store.File
	var err error
	if strings.TrimSpace(userID) == "" {
		drafts, err = store.ListDraftFilesForConversation(ctx, db, convID)
	} else {
		drafts, err = store.ListDraftFilesForConversationForUser(ctx, db, convID, userID)
	}
	if err != nil {
		return err
	}
	for _, draft := range drafts {
		if !attachedFiles[draft.ID] {
			return errors.New("conversation has unsent attachments; reload and try again")
		}
	}
	if len(docIDs) > 0 {
		statuses, err := store.ConversationDocumentStatuses(ctx, db, convID, docIDs)
		if err != nil {
			return err
		}
		for _, id := range docIDs {
			status, ok := statuses[id]
			if !ok {
				return errors.New("attached document not found")
			}
			if status != "ready" {
				return fmt.Errorf("attached document is still indexing (%s)", status)
			}
		}
	}
	if len(fileIDs) > 0 {
		statuses, err := store.ConversationDocumentStatusesForFiles(ctx, db, convID, fileIDs)
		if err != nil {
			return err
		}
		// The client's attachment.kind is untrusted and can drift from the server's
		// classification — most notably an .xlsx, whose OOXML MIME carries an
		// "officedocument" substring that trips browser-side /doc/ heuristics into
		// labelling it 'doc', while the backend files it as 'sheet' (sandbox data,
		// no RAG document row). Resolve the SERVER kind so "no document" is only an
		// error for files that were actually supposed to be ingested; spreadsheets
		// and images legitimately have none and must not block the send (the old
		// behaviour 409'd every xlsx upload with "attached document not found").
		serverKinds, err := store.ConversationFileKinds(ctx, db, convID, fileIDs)
		if err != nil {
			return err
		}
		for _, id := range fileIDs {
			fileStatuses := statuses[id]
			if len(fileStatuses) == 0 {
				// A file that genuinely doesn't exist (unknown id) or that IS a
				// document-kind but has no ingested document is a real problem —
				// keep rejecting it. A file the server filed as a non-document kind
				// (sheet → sandbox, image → vision) legitimately has no document and
				// must pass regardless of what the client called it.
				kind, known := serverKinds[id]
				if !known || isDocKind(kind) {
					return errors.New("attached document not found")
				}
				continue
			}
			for _, status := range fileStatuses {
				if status != "ready" {
					return fmt.Errorf("attached document is still indexing (%s)", status)
				}
			}
		}
	}
	return nil
}

// regenerateHandler creates a sibling assistant message under the SAME user
// parent — the §4.15 design: "regenerate forks at the assistant, never at the
// user". We do NOT copy the user turn into a new sibling; we simply run the
// orchestrator with the user message id as the parent so a new assistant
// child is produced. The branch picker on the assistant message then shows
// "1/2" / "2/2" between the previous reply and the new one.
//
// Implementation detail: the orchestrator's Run signature requires a UserText
// because it always inserts a user turn first. We sidestep that by injecting a
// flag in the request — when reusing an existing user message, the
// orchestrator must not create a new one.
func regenerateHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	var body struct {
		AssistantID         string          `json:"assistant_id"`
		GenerationID        string          `json:"generation_id"`
		ModelID             string          `json:"model_id"`
		Mode                string          `json:"mode"`
		Verify              bool            `json:"verify"`
		ToolMode            json.RawMessage `json:"tool_mode"`
		NoTools             bool            `json:"no_tools"`
		WebSearch           bool            `json:"web_search"`
		SelectedToolIDs     json.RawMessage `json:"selected_tool_ids"`
		KBIDs               json.RawMessage `json:"kb_ids"`
		Fast                bool            `json:"fast"` // §fast-mode: honour the CURRENT picker (regenerate follows the live toggle)
		ParamOverrides      map[string]any  `json:"params"`
		OptimizeImagePrompt *bool           `json:"optimize_image_prompt"`
		Locale              string          `json:"locale"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if body.GenerationID != "" && !validGenerationID(body.GenerationID) {
		writeError(w, 400, errors.New("invalid generation_id"))
		return
	}
	conv, err := store.GetConversation(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	permissionSnapshot, err := requestPermissionSnapshotFor(d, r)
	if err != nil {
		writeError(w, http.StatusForbidden, errPermissionDenied)
		return
	}
	permissions := permissionSnapshot.Permissions
	if !permissions.AllowKnowledgeBases && turnUsesKnowledgeBase(conv, body.KBIDs) {
		writeError(w, http.StatusForbidden, errKnowledgeBaseGroupPermission)
		return
	}
	turnKBIDs, turnKBSelectionConfigured, err := resolveTurnKnowledgeBaseSelection(
		r.Context(), d.DB, u.ID, conv, body.KBIDs,
	)
	if err != nil {
		switch {
		case errors.Is(err, errInvalidInput):
			writeError(w, http.StatusBadRequest, err)
		case errors.Is(err, store.ErrMixedKBEmbeddingModels):
			writeError(w, http.StatusConflict, errKnowledgeBaseSelectionIncompatible)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	// §workspace RBAC phase 4: regenerated turns pass the same workspace
	// capability gate as fresh ones (model allowlist, image switch, credit
	// limit; attachments were already persisted upstream). A model-less
	// request keeps the orchestrator's own error path.
	{
		effectiveModel, modelErr := resolveEffectiveConversationModel(r.Context(), d.DB, conv, body.ModelID, body.Fast)
		if modelErr != nil && !errors.Is(modelErr, errNoModelConfigured) {
			writeError(w, imageCapabilityErrorStatus(modelErr), modelErr)
			return
		}
		if err := enforceWorkspaceTurnPolicy(r.Context(), d.DB, conv, u.ID, effectiveModel, 0,
			turnUsesKnowledgeBase(conv, body.KBIDs)); err != nil {
			writeError(w, workspacePolicyErrorStatus(err), err)
			return
		}
	}
	workspaceAccess, workspaceAccessErr := captureGenerationWorkspaceAccess(r.Context(), d.DB, conv, u.ID)
	if workspaceAccessErr != nil {
		if errors.Is(workspaceAccessErr, errForbidden) {
			writeError(w, http.StatusForbidden, errForbidden)
		} else {
			writeError(w, http.StatusInternalServerError, workspaceAccessErr)
		}
		return
	}
	knowledgeBaseAccess := captureGenerationKnowledgeBaseAccess(
		d, u.ID, id, conv.WorkspaceID,
		generationKnowledgeBaseIDs(r.Context(), d.DB, u.ID, conv, turnKBIDs, turnKBSelectionConfigured),
	)
	toolMode, err := resolveTurnToolMode(body.ToolMode, body.NoTools, effectiveDefaultToolMode(d.DB, u.Settings))
	if err != nil {
		writeError(w, 400, err)
		return
	}
	selectedToolIDs, selectedToolsConfigured, err := parseSelectedToolIDs(body.SelectedToolIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	selectedToolIDs, selectedToolsConfigured = applyTurnToolPermissions(permissions, selectedToolIDs, selectedToolsConfigured)
	if !toolPolicyAllowsID(permissions, "builtin:aivory_web_search") {
		body.WebSearch = false
	}
	if !permissions.AllowDrawing {
		model, modelErr := resolveEffectiveConversationModel(r.Context(), d.DB, conv, body.ModelID, body.Fast)
		if modelErr != nil {
			writeError(w, imageCapabilityErrorStatus(modelErr), modelErr)
			return
		}
		if model.Kind == "image" {
			writeError(w, http.StatusForbidden, errDrawingGroupPermission)
			return
		}
	}
	// §fast-mode overrides all other turn flags (see postMessageHandler).
	if body.Fast {
		body.Mode = ""
		body.Verify = false
		toolMode = llm.ToolModeEnabled
		body.WebSearch = false
	}
	// Keep regenerate aligned with the normal send path: users without the
	// Deep Research group feature cannot force it by calling /regenerate.
	if body.Mode == "deep-research" && u.Role != "admin" && !userGroupHasFeature(r.Context(), d, permissionSnapshot.GroupID, "research") {
		body.Mode = ""
	}
	toolMode, body.WebSearch = normalizeTurnFlags(body.Mode, toolMode, body.WebSearch)
	// §8/§C7 daily-message + token + concurrent-gen quotas apply to regenerate
	// too — otherwise repeated /regenerate bypasses the per-day message cap.
	// Reserve the concurrent-gen slot FIRST so a slot-full 429 doesn't burn a
	// daily-message count for a turn that never ran.
	release, ok := reserveConcurrentGen(d, u.ID)
	if !ok {
		writeError(w, 429, errors.New("too many concurrent generations"))
		return
	}
	defer release()
	if u.Role != "admin" {
		if !checkDailyMessageLimit(d, u.ID) {
			writeError(w, 429, errors.New("daily message limit reached"))
			return
		}
		if !checkDailyTokenQuota(d, u.ID) {
			writeError(w, 429, errors.New("daily token quota reached"))
			return
		}
	}
	if body.AssistantID == "" {
		body.AssistantID = conv.ActiveLeafID
	}
	if body.AssistantID == "" {
		writeError(w, 400, errors.New("assistant_id required"))
		return
	}
	assistant, err := store.GetMessage(r.Context(), d.DB, body.AssistantID)
	if err != nil || assistant.ConversationID != id || assistant.Role != "assistant" {
		writeError(w, 404, errNotFound)
		return
	}
	user, err := store.GetMessage(r.Context(), d.DB, assistant.ParentID)
	if err != nil || user.Role != "user" {
		writeError(w, 404, errNotFound)
		return
	}
	var persistedSkillIDs []string
	_ = json.Unmarshal(user.SelectedUserSkillIDs, &persistedSkillIDs)
	if _, _, skillErr := resolvePermittedUserSkillSelection(
		r.Context(), d.DB, u.ID, conv.WorkspaceID, persistedSkillIDs, false, permissions.Skills,
	); skillErr != nil {
		if errors.Is(skillErr, errSkillGroupPermission) {
			writeError(w, http.StatusForbidden, errSkillGroupPermission)
		} else {
			writeError(w, http.StatusInternalServerError, skillErr)
		}
		return
	}
	// Extract text from the parent user message — purely so the orchestrator's
	// existing prompt path has a UserText to reference. The new assistant
	// message will be parented to `user.ID`, NOT to a new sibling.
	var blocks []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(user.Blocks, &blocks)
	text := ""
	for _, b := range blocks {
		if b.Kind == "text" {
			text += b.Text + "\n"
		}
	}
	text = strings.TrimSpace(text)

	writer := sse.New(w)
	if writer == nil {
		writeError(w, 500, errors.New("streaming not supported"))
		return
	}
	// The reply must survive the user closing the page mid-stream: detach the
	// generation from the HTTP request so a browser disconnect no longer aborts
	// (and loses) the answer — it finishes server-side and is persisted, ready
	// when the user returns. Only an explicit stop/kill or the hard time cap can
	// cancel it now.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), generationcfg.MaxDuration())
	defer cancel()
	accessRevocation := newGenerationAccessRevocationWatcher(
		d, ctx, cancel, id, conv.WorkspaceID, &permissionSnapshot, knowledgeBaseAccess, workspaceAccess,
	)
	defer accessRevocation.close()
	scopedStop := newGenerationStopWatcher(d, ctx, cancel, u.ID, id, body.GenerationID)
	defer scopedStop.close()
	stopCh, unsub := d.Cache.Subscribe(conversationStopTopic(u.ID, id))
	defer unsub()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	// §6.2: 15s heartbeat to keep proxies from closing the SSE channel.
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go func() {
		t := time.NewTicker(ssePingHeartbeatRegenerate)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
				writer.Ping()
			}
		}
	}()
	streamMessageID := ""
	terminalSent := false
	sendEvent := func(ev llm.SseEvent) {
		if ev.Type == "message_start" && ev.MessageID != "" {
			streamMessageID = ev.MessageID
			scopedStop.watchMessage(streamMessageID)
			scopedStop.activate()
			accessRevocation.watchMessage(streamMessageID)
		}
		if ctx.Err() != nil && ev.Type != "message_start" && (ev.Type != "done" || ev.StopReason != "stopped") {
			return
		}
		if streamMessageID != "" && ev.MessageID == "" {
			ev.MessageID = streamMessageID
		}
		if genstream.Terminal(ev) {
			terminalSent = true
		}
		if streamMessageID != "" {
			eventID, appended, revoked := genstream.Append(
				d.Cache, streamMessageID, ev,
				generationStreamDenyKeysForAccess(conv.WorkspaceID, knowledgeBaseAccess)...,
			)
			if revoked {
				cancel()
				if ev.Type == "done" && ev.StopReason == "stopped" {
					_ = writer.Send(ev, ev.Type)
				}
				return
			}
			if appended {
				_ = writer.SendID(ev, ev.Type, eventID)
				return
			}
			if conv.WorkspaceID != "" {
				cancel()
				return
			}
		}
		if ctx.Err() != nil && ev.Type != "message_start" && (ev.Type != "done" || ev.StopReason != "stopped") {
			return
		}
		_ = writer.Send(ev, ev.Type)
	}

	runResult, runErr := d.Orchestrator.Run(ctx, llm.RunRequest{
		UserID:                   u.ID,
		ConversationID:           id,
		ModelID:                  body.ModelID,
		UserText:                 text,
		ParentID:                 user.ID, // assistant sibling under SAME user — §4.15
		ReuseExistingUserMessage: true,
		Mode:                     body.Mode,
		Verify:                   body.Verify,
		ToolMode:                 toolMode,
		SelectedToolIDs:          selectedToolIDs,
		SelectedToolsConfigured:  selectedToolsConfigured,
		ToolAccessPolicy:         workspaceTurnToolPolicy(r.Context(), d.DB, conv, runToolAccessPolicy(permissions)),
		WorkspaceAccessCheck: func(checkCtx context.Context) error {
			if workspaceAccess.stillCurrent(checkCtx, d.DB) {
				return nil
			}
			return errors.New("workspace access revoked")
		},
		ForceWebSearch:                   body.WebSearch,
		Fast:                             body.Fast,
		ParamOverrides:                   body.ParamOverrides,
		OptimizeImagePrompt:              body.OptimizeImagePrompt,
		Locale:                           body.Locale,
		KnowledgeBaseIDs:                 turnKBIDs,
		KnowledgeBaseSelectionConfigured: turnKBSelectionConfigured,
	}, sendEvent)
	err = runErr
	if streamMessageID == "" && runResult != nil && runResult.AssistantMessage != nil {
		streamMessageID = runResult.AssistantMessage.ID
	}
	if !accessRevocation.permissionAccessStillCurrent() {
		accessRevocation.revokePermissionAccess()
	}
	if !accessRevocation.knowledgeBaseAccessStillCurrent() {
		accessRevocation.revokeKnowledgeBaseAccess()
	}
	if accessRevocation.permissionRevoked.Load() {
		accessRevocation.scrubRevokedMessage(streamMessageID, "permission")
	}
	if accessRevocation.knowledgeRevoked.Load() {
		accessRevocation.scrubRevokedMessage(streamMessageID, "knowledge-base")
	}
	if ctx.Err() != nil && !terminalSent {
		sendEvent(llm.SseEvent{Type: "done", MessageID: streamMessageID, StopReason: "stopped"})
		publishUserEvent(d, r, u.ID, "conversation.updated", id)
		return
	}
	if err != nil && !terminalSent {
		logChatRunError(d.Logger, chatRunErrorMetadata{
			Operation:      "regenerate",
			UserID:         u.ID,
			ConversationID: id,
			Fast:           body.Fast,
			Branch:         true,
			ParentID:       user.ID,
			ReferenceID:    body.AssistantID,
		}, err)
		sendEvent(chatRunErrorEvent(err, streamMessageID))
	}
	// §23: regeneration finished — nudge the user's other devices.
	publishUserEvent(d, r, u.ID, "conversation.updated", id)
}

// streamMessageHandler replays and follows the generation stream for one
// assistant message. It is keyed by assistant message id (not conversation id),
// so two concurrent branches in the same conversation cannot interleave frames.
func streamMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	msgID := pathParam(r, "msgId")
	conv, err := store.GetConversation(r.Context(), d.DB, convID, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	msg, err := store.GetMessage(r.Context(), d.DB, msgID)
	if err != nil || msg.ConversationID != convID || msg.Role != "assistant" {
		writeError(w, 404, errNotFound)
		return
	}
	writer := sse.New(w)
	if writer == nil {
		writeError(w, 500, errors.New("streaming not supported"))
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	accessRevocation := newGenerationAccessRevocationWatcher(d, ctx, cancel, convID, conv.WorkspaceID, nil, nil, nil)
	defer accessRevocation.close()
	accessRevocation.watchMessage(msgID)

	lastID := r.Header.Get("Last-Event-ID")
	if lastID == "" {
		lastID = r.URL.Query().Get("last_id")
	}
	terminal := false
	flush := func() (done, revoked bool) {
		events, available, streamRevoked := genstream.Read(
			d.Cache, msgID, lastID, streamReplayBatchSize,
			generationStreamDenyKeys(conv.WorkspaceID)...,
		)
		if streamRevoked {
			return true, true
		}
		if !available {
			_ = writer.Send(llm.SseEvent{Type: "error", MessageID: msgID, Message: "stream replay unavailable"}, "error")
			return true, false
		}
		for _, ev := range events {
			lastID = ev.ID
			if genstream.Terminal(ev.Value) {
				terminal = true
			}
			_ = writer.SendID(ev.Value, ev.Value.Type, ev.ID)
		}
		return terminal, false
	}
	if done, revoked := flush(); done {
		if revoked {
			_ = writer.Send(llm.SseEvent{Type: "done", MessageID: msgID, StopReason: "stopped"}, "done")
		}
		return
	}
	if msg.Status != "streaming" {
		if !terminal {
			_ = writer.Send(llm.SseEvent{Type: "done", MessageID: msgID, StopReason: msg.StopReason, Credits: msg.Credits}, "done")
		}
		return
	}

	ch, unsub := d.Cache.Subscribe(genstream.Topic(msgID))
	defer unsub()
	if done, revoked := flush(); done {
		if revoked {
			_ = writer.Send(llm.SseEvent{Type: "done", MessageID: msgID, StopReason: "stopped"}, "done")
		}
		return
	}
	ping := time.NewTicker(ssePingHeartbeatStream)
	defer ping.Stop()
	statusCheck := time.NewTicker(streamStatusRecheckInterval)
	defer statusCheck.Stop()
	for {
		select {
		case <-ctx.Done():
			if r.Context().Err() == nil {
				_ = writer.Send(llm.SseEvent{Type: "done", MessageID: msgID, StopReason: "stopped"}, "done")
			}
			return
		case <-ch:
			if done, revoked := flush(); done {
				if revoked {
					_ = writer.Send(llm.SseEvent{Type: "done", MessageID: msgID, StopReason: "stopped"}, "done")
				}
				return
			}
		case <-ping.C:
			writer.Ping()
		case <-statusCheck.C:
			if done, revoked := flush(); done {
				if revoked {
					_ = writer.Send(llm.SseEvent{Type: "done", MessageID: msgID, StopReason: "stopped"}, "done")
				}
				return
			}
			fresh, ferr := store.GetMessage(ctx, d.DB, msgID)
			if ferr == nil && fresh.Status != "streaming" {
				if !terminal {
					_ = writer.Send(llm.SseEvent{Type: "done", MessageID: msgID, StopReason: fresh.StopReason, Credits: fresh.Credits}, "done")
				}
				return
			}
		}
	}
}

// userGroupHasFeature reports whether the user's group carries a capability
// flag (e.g. "research"). Missing group / parse error → not entitled.
func userGroupHasFeature(ctx context.Context, d Deps, groupID, feature string) bool {
	if groupID == "" {
		groupID = store.DefaultGroupID
	}
	g, err := store.GetUserGroup(ctx, d.DB, groupID)
	if err != nil || g == nil {
		return false
	}
	var feats []string
	if json.Unmarshal(g.Features, &feats) != nil {
		return false
	}
	for _, f := range feats {
		if f == feature {
			return true
		}
	}
	return false
}

// nextMidnightUTC returns the next UTC midnight, used to set quota key TTLs so
// they expire at the start of the next calendar day rather than "24 hours from
// first use" (H-13).
func nextMidnightUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}

func checkDailyMessageLimit(d Deps, userID string) bool {
	// H-13: read the limit BEFORE incrementing the counter so a limit of 0
	// (disabled) never burns a count, and so the check reflects the true intent.
	limit := 200
	if raw, err := store.GetSetting(d.DB, "daily_message_limit"); err == nil {
		_ = json.Unmarshal(raw, &limit)
	}
	if limit <= 0 {
		return true // 0 = unlimited; don't touch the counter at all
	}
	key := "quota:" + userID + ":" + time.Now().UTC().Format("2006-01-02")
	ttl := time.Until(nextMidnightUTC())
	n := d.Cache.Incr(key, ttl)
	return int(n) <= limit
}

// editMessageHandler edits a user question or assistant reply's visible text IN
// PLACE. User-message authorship remains protected in shared workspaces;
// assistant replies belong to the shared conversation and may be edited by any
// member who can access it.
func editMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	msgID := pathParam(r, "msgId")
	if _, err := store.GetConversation(r.Context(), d.DB, convID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeError(w, 400, errors.New("text required"))
		return
	}
	msg, err := store.GetMessage(r.Context(), d.DB, msgID)
	if err != nil || msg.ConversationID != convID || (msg.Role != "user" && msg.Role != "assistant") {
		writeError(w, 404, errNotFound)
		return
	}
	// Fast fail for explicit foreign user-message authors. The authoritative
	// author/legacy-owner and current-membership checks run atomically in store.
	if msg.Role == "user" && msg.AuthorID != "" && msg.AuthorID != u.ID {
		writeError(w, 404, errNotFound)
		return
	}
	var blocks json.RawMessage
	if msg.Role == "assistant" {
		blocks, err = replaceAssistantReplyText(msg.Blocks, body.Text)
		if err != nil {
			writeError(w, 500, err)
			return
		}
	} else {
		blocks, _ = json.Marshal([]llm.UnifiedBlock{{Kind: "text", Text: body.Text}})
	}
	if err := store.UpdateMessageContentForUser(r.Context(), d.DB, convID, u.ID, msgID, blocks); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	msgcache.Bump(d.Cache, convID)
	updated, _ := store.GetMessage(r.Context(), d.DB, msgID)
	publishUserEvent(d, r, u.ID, "conversation.updated", convID)
	// This endpoint bypasses the redactCost chokepoint, so apply the same
	// user-boundary redaction here: strip admin-only cost/raw, and §fast-mode blank
	// the real model identity on a fast turn (a fast user row is stamped with the
	// hidden fast model's id/label/provider — never return them).
	if updated == nil {
		writeJSON(w, 200, updated)
		return
	}
	hydrated := userMessageResponse(d, r, []store.Message{*updated})
	writeJSON(w, 200, hydrated[0])
}

// replaceAssistantReplyText overwrites exactly the Markdown that MessageRow
// renders as the final answer: text blocks after the last tool call (or all text
// blocks on a tool-free reply). Thinking, tool, research, image and artifact
// blocks remain intact.
func replaceAssistantReplyText(raw json.RawMessage, text string) (json.RawMessage, error) {
	var blocks []llm.UnifiedBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("decode assistant blocks: %w", err)
	}
	lastToolCall := -1
	for i, block := range blocks {
		if block.Kind == "tool_call" {
			lastToolCall = i
		}
	}
	next := make([]llm.UnifiedBlock, 0, len(blocks)+1)
	for i, block := range blocks {
		if block.Kind == "text" && i > lastToolCall {
			continue
		}
		next = append(next, block)
	}
	next = append(next, llm.UnifiedBlock{Kind: "text", Text: text})
	return json.Marshal(next)
}

// deleteMessageHandler deletes ONE conversational round (the user question + all
// of its assistant answers) given any message id inside it. Branch-safe: earlier
// turns, later turns, and sibling branches are preserved (see store.DeleteRound).
// Returns the conversation's new active leaf + the refreshed active-path messages.
func deleteMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	msgID := pathParam(r, "msgId")
	permissions, permissionErr := requestPermissions(d, r)
	if permissionErr != nil || !permissions.AllowConversationDeletion {
		writeError(w, http.StatusForbidden, errForbidden)
		return
	}
	conv, err := store.GetConversation(r.Context(), d.DB, convID, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	// §workspaces: deleting a round in a shared conversation is limited to the
	// round's author or the conversation creator. Resolve the round's USER turn
	// (clicking an answer implies its question) and check its author.
	if conv.WorkspaceID != "" && conv.UserID != u.ID {
		if m, merr := store.GetMessage(r.Context(), d.DB, msgID); merr == nil && m.ConversationID == convID {
			author := m.AuthorID
			if m.Role != "user" && m.ParentID != "" {
				if pu, perr := store.GetMessage(r.Context(), d.DB, m.ParentID); perr == nil && pu.Role == "user" {
					author = pu.AuthorID
				}
			}
			if author == "" || author != u.ID {
				writeError(w, 404, errNotFound)
				return
			}
		} else {
			writeError(w, 404, errNotFound)
			return
		}
	}
	newLeaf, err := store.DeleteRound(r.Context(), d.DB, convID, u.ID, msgID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	msgcache.Bump(d.Cache, convID)
	msgs, err := msgcache.ListMessages(r.Context(), d.Cache, d.DB, convID, newLeaf)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// Enrich with sibling/branch metadata + redact admin-only cost, exactly like
	// getConversationHandler — otherwise the swapped-in path loses its `< n/m >`
	// branch picker and leaks per-message cost to the user.
	publishUserEvent(d, r, u.ID, "conversation.updated", convID)
	writeJSON(w, 200, map[string]any{"ok": true, "active_leaf_id": newLeaf, "messages": userMessageResponse(d, r, msgs)})
}

// feedbackMessageHandler stores the authenticated user's rating of an assistant
// message. Dislikes may carry optional structured reasons and a short note.
func feedbackMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	msgID := pathParam(r, "msgId")
	conv, err := store.GetConversation(r.Context(), d.DB, convID, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	var body struct {
		Feedback string   `json:"feedback"`
		Reasons  []string `json:"reasons"`
		Comment  string   `json:"comment"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if body.Feedback != "" && body.Feedback != "like" && body.Feedback != "dislike" {
		writeError(w, 400, errors.New("feedback must be 'like', 'dislike', or empty"))
		return
	}
	body.Comment = strings.TrimSpace(body.Comment)
	validatedReasons, err := store.NormalizeMessageFeedbackReasons(body.Reasons)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	body.Reasons = validatedReasons
	if len([]rune(body.Comment)) > store.MessageFeedbackCommentMaxRunes {
		writeError(w, 400, fmt.Errorf("feedback comment must be at most %d characters", store.MessageFeedbackCommentMaxRunes))
		return
	}
	if body.Feedback != store.MessageFeedbackDislike {
		body.Reasons = []string{}
		body.Comment = ""
	}
	msg, err := store.GetMessage(r.Context(), d.DB, msgID)
	if err != nil || msg.ConversationID != convID || msg.Role != "assistant" {
		writeError(w, 404, errNotFound)
		return
	}
	channelID, err := store.MessageFeedbackChannelID(r.Context(), d.DB, msgID, msg.ModelID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	stored, err := store.SetMessageFeedbackForUser(r.Context(), d.DB, store.MessageFeedback{
		MessageID:      msgID,
		ConversationID: convID,
		UserID:         u.ID,
		WorkspaceID:    conv.WorkspaceID,
		ModelID:        msg.ModelID,
		ChannelID:      channelID,
		Rating:         body.Feedback,
		Reasons:        body.Reasons,
		Comment:        body.Comment,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	msgcache.Bump(d.Cache, convID)
	responseReasons := []string{}
	comment := ""
	if stored != nil {
		responseReasons = stored.Reasons
		comment = stored.Comment
	}
	writeJSON(w, 200, map[string]any{
		"ok":               true,
		"feedback":         body.Feedback,
		"feedback_reasons": responseReasons,
		"feedback_comment": comment,
	})
}
