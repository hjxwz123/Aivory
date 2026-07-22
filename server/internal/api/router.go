// Package api wires the HTTP API. The router is plain net/http with a tiny
// custom mux to keep the dependency surface small. Each handler module owns
// the endpoints for one feature area (auth, conversations, projects, files,
// kbs, admin, etc.).
package api

import (
	"database/sql"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/envcfg"
	"aivory/server/internal/llm"
	"aivory/server/internal/mail"
	"aivory/server/internal/queue"
	"aivory/server/internal/rag"
	"aivory/server/internal/tools"
)

// Deps is the dependency bundle passed to NewRouter.
type Deps struct {
	Config       config.Config
	DB           *sql.DB
	Cache        cache.Cache
	Queue        queue.Queue
	Auth         *auth.Service
	Mailer       mail.Sender
	Providers    *llm.Registry
	Tools        *tools.Registry
	RAG          *rag.Service
	Orchestrator *llm.Orchestrator
	Logger       *log.Logger
}

// Env-overridable per-IP rate-limit budgets ("<N> per <window>") and the CORS
// preflight cache age. Defaults match the historical hardcoded values; each is
// tunable via the paired AIVORY_API_RATE_LIMIT_*_MAX / *_WINDOW variables (see
// docs/config-reference.md) without changing behaviour when unset.
var (
	rlRegisterMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_REGISTER_MAX", 5)
	rlRegisterWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_REGISTER_WINDOW", 60*time.Second)

	rlLoginMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_LOGIN_MAX", 10)
	rlLoginWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_LOGIN_WINDOW", 60*time.Second)

	rlLogin2faMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_LOGIN_2FA_MAX", 10)
	rlLogin2faWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_LOGIN_2FA_WINDOW", 60*time.Second)

	rlLogoutMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_LOGOUT_MAX", 30)
	rlLogoutWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_LOGOUT_WINDOW", 60*time.Second)

	rlRefreshMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_REFRESH_MAX", 30)
	rlRefreshWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_REFRESH_WINDOW", 60*time.Second)

	rlVerifyEmailMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_VERIFY_EMAIL_MAX", 10)
	rlVerifyEmailWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_VERIFY_EMAIL_WINDOW", 5*60*time.Second)

	rlSendCodeMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_SEND_CODE_MAX", 3)
	rlSendCodeWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_SEND_CODE_WINDOW", 60*time.Second)

	rlForgotPasswordMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_FORGOT_PASSWORD_MAX", 5)
	rlForgotPasswordWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_FORGOT_PASSWORD_WINDOW", 15*60*time.Second)

	rlResetPasswordMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_RESET_PASSWORD_MAX", 5)
	rlResetPasswordWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_RESET_PASSWORD_WINDOW", 60*time.Second)

	rlCaptchaIssueMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_CAPTCHA_ISSUE_MAX", 30)
	rlCaptchaIssueWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_CAPTCHA_ISSUE_WINDOW", 60*time.Second)

	rlCaptchaVerifyMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_CAPTCHA_VERIFY_MAX", 60)
	rlCaptchaVerifyWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_CAPTCHA_VERIFY_WINDOW", 60*time.Second)

	rlFirstRunSetupMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_FIRST_RUN_SETUP_MAX", 10)
	rlFirstRunSetupWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_FIRST_RUN_SETUP_WINDOW", 60*time.Second)

	rlPublicSharedConversationMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_PUBLIC_SHARED_CONVERSATION_MAX", 60)
	rlPublicSharedConversationWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_PUBLIC_SHARED_CONVERSATION_WINDOW", 60*time.Second)

	rlSharedAssetsMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_SHARED_ASSETS_FILES_ARTIFACTS_MAX", 240)
	rlSharedAssetsWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_SHARED_ASSETS_FILES_ARTIFACTS_WINDOW", 60*time.Second)

	rlOauthMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_OAUTH_START_CALLBACK_HANDOFF_MAX", 20)
	rlOauthWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_OAUTH_START_CALLBACK_HANDOFF_WINDOW", 60*time.Second)

	rlPasswordChangeMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_PASSWORD_CHANGE_SET_MAX", 5)
	rlPasswordChangeWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_PASSWORD_CHANGE_SET_WINDOW", 60*time.Second)

	rlIdentityLinkStartMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_IDENTITY_LINK_START_MAX", 20)
	rlIdentityLinkStartWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_IDENTITY_LINK_START_WINDOW", 60*time.Second)

	rl2faSetupMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_2FA_SETUP_ENABLE_DISABLE_MAX", 10)
	rl2faSetupWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_2FA_SETUP_ENABLE_DISABLE_WINDOW", 5*60*time.Second)

	rlRedeemCodeMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_REDEEM_CODE_MAX", 10)
	rlRedeemCodeWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_REDEEM_CODE_WINDOW", 60*time.Second)

	rlWorkspaceJoinMax    = envcfg.Int("AIVORY_API_RATE_LIMIT_WORKSPACE_JOIN_MAX", 30)
	rlWorkspaceJoinWindow = envcfg.Dur("AIVORY_API_RATE_LIMIT_WORKSPACE_JOIN_WINDOW", 60*time.Second)

	corsPreflightMaxAge = 600 * time.Second
)

// NewRouter returns the fully-wired http.Handler.
func NewRouter(d Deps) http.Handler {
	// Async title generation persists after the request's chat SSE has finished.
	// Bridge its committed metadata updates into the same realtime stream used by
	// cross-device conversation changes; background work has no originating tab.
	if d.Orchestrator != nil {
		d.Orchestrator.SetConversationUpdatedHandler(func(userID, conversationID string) {
			publishUserEvent(d, nil, userID, "conversation.updated", conversationID)
		})
	}

	// Resume account deletions stranded in status='deleting' by a previous
	// process (§async user delete). Async: never delays startup.
	go resumeUserDeletions(d)

	mux := newMux()

	// Public endpoints.
	// §8 brute-force defence: tight IP-scoped rate limit on auth surfaces.
	// 10 requests per IP per 60s — enough for retries on typos, not enough
	// to enumerate passwords / spam refresh.
	mux.handle("POST", "/api/auth/register", rateLimitedIP(d, "auth", rlRegisterMax, rlRegisterWindow, wrap(d, registerHandler)))
	mux.handle("POST", "/api/auth/login", rateLimitedIP(d, "auth", rlLoginMax, rlLoginWindow, wrap(d, loginHandler)))
	mux.handle("POST", "/api/auth/login/2fa", rateLimitedIP(d, "auth", rlLogin2faMax, rlLogin2faWindow, wrap(d, login2faHandler)))
	mux.handle("POST", "/api/auth/logout", rateLimitedIP(d, "auth", rlLogoutMax, rlLogoutWindow, wrap(d, logoutHandler)))
	mux.handle("POST", "/api/auth/refresh", rateLimitedIP(d, "auth", rlRefreshMax, rlRefreshWindow, wrap(d, refreshHandler)))
	mux.handle("POST", "/api/auth/verify-email", rateLimitedIP(d, "verify-email", rlVerifyEmailMax, rlVerifyEmailWindow, wrap(d, verifyEmailHandler)))
	mux.handle("POST", "/api/auth/send-code", rateLimitedIP(d, "auth", rlSendCodeMax, rlSendCodeWindow, wrap(d, sendCodeHandler)))
	mux.handle("POST", "/api/auth/forgot-password", rateLimitedIP(d, "forgot-password", rlForgotPasswordMax, rlForgotPasswordWindow, wrap(d, forgotPasswordHandler)))
	mux.handle("POST", "/api/auth/reset-password", rateLimitedIP(d, "auth", rlResetPasswordMax, rlResetPasswordWindow, wrap(d, resetPasswordHandler)))
	mux.handle("GET", "/api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.handle("GET", "/api/public/signup-open", wrap(d, signupOpenHandler))
	// Slider-puzzle captcha for registration. GET issues a challenge; POST /verify
	// checks a solution and returns a single-use pass token (immediate feedback).
	mux.handle("GET", "/api/public/captcha", rateLimitedIP(d, "auth", rlCaptchaIssueMax, rlCaptchaIssueWindow, wrap(d, captchaHandler)))
	mux.handle("POST", "/api/public/captcha/verify", rateLimitedIP(d, "auth", rlCaptchaVerifyMax, rlCaptchaVerifyWindow, wrap(d, captchaVerifyHandler)))
	// First-run setup (§ first-run setup): public probe + create-first-admin.
	mux.handle("GET", "/api/public/needs-setup", wrap(d, needsSetupHandler))
	mux.handle("POST", "/api/setup", rateLimitedIP(d, "auth", rlFirstRunSetupMax, rlFirstRunSetupWindow, wrap(d, setupHandler)))
	mux.handle("GET", "/api/public/oauth-providers", wrap(d, oauthProvidersPublicHandler))
	// Membership tiers for the public landing page (§ user groups) — read-only,
	// marketing info (names / prices / features / allowances), no secrets.
	mux.handle("GET", "/api/public/user-groups", wrap(d, listUserGroupsPublic))
	// Public read-only conversation share (token in the path; no auth). Rate
	// limited (§D1) so the token space can't be swept even though it's now 192-bit.
	mux.handle("GET", "/api/public/shared/:token", rateLimitedIP(d, "share", rlPublicSharedConversationMax, rlPublicSharedConversationWindow, wrap(d, publicSharedHandler)))
	// Share-scoped assets: uploaded attachments + generated artifacts referenced
	// by the snapshot (the private /api/files|artifacts routes need the owner's
	// session, which share viewers don't have). Membership in the snapshot is the
	// access check; a separate, roomier rate bucket since one page load can pull
	// many images.
	mux.handle("GET", "/api/public/shared/:token/files/:id", rateLimitedIP(d, "share_asset", rlSharedAssetsMax, rlSharedAssetsWindow, wrap(d, publicSharedFileHandler)))
	mux.handle("GET", "/api/public/shared/:token/artifacts/:id", rateLimitedIP(d, "share_asset", rlSharedAssetsMax, rlSharedAssetsWindow, wrap(d, publicSharedArtifactHandler)))

	// OAuth / social login. /start is a top-level browser navigation; /callback
	// is hit by the provider redirect (GET) or Apple's form_post (POST).
	mux.handle("GET", "/api/auth/oauth/:id/start", rateLimitedIP(d, "auth", rlOauthMax, rlOauthWindow, wrap(d, oauthStartHandler)))
	mux.handle("GET", "/api/auth/oauth/:id/callback", rateLimitedIP(d, "auth", rlOauthMax, rlOauthWindow, wrap(d, oauthCallbackHandler)))
	mux.handle("POST", "/api/auth/oauth/:id/callback", rateLimitedIP(d, "auth", rlOauthMax, rlOauthWindow, wrap(d, oauthCallbackHandler)))
	// §cross-domain hand-off: redeems the one-time token minted by the canonical
	// callback and sets the session cookies on THIS (origin) domain.
	mux.handle("GET", "/api/auth/oauth/handoff", rateLimitedIP(d, "auth", rlOauthMax, rlOauthWindow, wrap(d, oauthHandoffHandler)))

	// Authenticated endpoints.
	// §23 realtime notify stream: one long-lived SSE connection per open tab;
	// pushes thin conversation-change events for multi-device live sync.
	mux.handle("GET", "/api/events", requireAuth(d, eventsStreamHandler))
	mux.handle("GET", "/api/me", requireAuth(d, meHandler))
	mux.handle("PATCH", "/api/me", requireAuth(d, updateMeHandler))
	mux.handle("DELETE", "/api/me", requireAuth(d, deleteMeHandler))
	// Password-change and 2FA endpoints are sensitive: rate-limit to slow
	// credential-stuffing attacks even when the attacker holds a valid session.
	// Per-IP window (not per-user) so a compromised token on a shared IP still
	// gets throttled.
	mux.handle("PATCH", "/api/me/password", rateLimitedIP(d, "password-change", rlPasswordChangeMax, rlPasswordChangeWindow, requireAuth(d, changePasswordHandler)))
	mux.handle("POST", "/api/me/password/set", rateLimitedIP(d, "password-change", rlPasswordChangeMax, rlPasswordChangeWindow, requireAuth(d, setPasswordHandler)))
	// User avatar upload — reuses the image-validating icon handler (returns
	// {url}); the client stores that URL in the user's settings (avatar_url).
	mux.handle("POST", "/api/me/avatar", requireAuth(d, uploadIconAdmin))
	mux.handle("GET", "/api/me/usage", requireAuth(d, meUsageHandler))
	mux.handle("GET", "/api/me/credits", requireAuth(d, meCreditsHandler))
	mux.handle("GET", "/api/me/settings", requireAuth(d, meSettingsHandler))
	mux.handle("PATCH", "/api/me/settings", requireAuth(d, updateMeSettingsHandler))
	mux.handle("GET", "/api/me/upload-policy", requireAuth(d, meUploadPolicyHandler))
	// §identity linking — bind/list/unbind third-party accounts on the current
	// user. Link-start is authenticated (it stashes the caller in the OAuth
	// state); the callback reuses the shared public /api/auth/oauth/:id/callback.
	mux.handle("GET", "/api/me/identities", requireAuth(d, listIdentitiesHandler))
	mux.handle("DELETE", "/api/me/identities", requireAuth(d, unlinkIdentityHandler))
	mux.handle("POST", "/api/me/identities/:id/link", rateLimitedIP(d, "auth", rlIdentityLinkStartMax, rlIdentityLinkStartWindow, requireAuth(d, oauthLinkStartHandler)))
	mux.handle("GET", "/api/announcement", requireAuth(d, announcementHandler))
	mux.handle("POST", "/api/me/2fa/setup", rateLimitedIP(d, "2fa", rl2faSetupMax, rl2faSetupWindow, requireAuth(d, twofaSetupHandler)))
	mux.handle("POST", "/api/me/2fa/enable", rateLimitedIP(d, "2fa", rl2faSetupMax, rl2faSetupWindow, requireAuth(d, twofaEnableHandler)))
	mux.handle("POST", "/api/me/2fa/disable", rateLimitedIP(d, "2fa", rl2faSetupMax, rl2faSetupWindow, requireAuth(d, twofaDisableHandler)))
	mux.handle("GET", "/api/me/memories", requireAuth(d, listMemoriesHandler))
	mux.handle("POST", "/api/me/memories", requireAuth(d, createMemoryHandler))
	mux.handle("PATCH", "/api/me/memories/:id", requireAuth(d, updateMemoryHandler))
	mux.handle("DELETE", "/api/me/memories/:id", requireAuth(d, deleteMemoryHandler))
	// Redeem a code → grants the user a configured user-group for a configured
	// duration (§ redeem codes). Tight rate limit so a stolen code can't be
	// brute-force-typed by an attacker who knows the alphabet.
	mux.handle("POST", "/api/me/redeem", rateLimitedIP(d, "redeem", rlRedeemCodeMax, rlRedeemCodeWindow, requireAuth(d, redeemCodeHandler)))

	// Active sessions (§ account → active sessions). Registered under /api/auth
	// so the refresh-token cookie (scoped to /api/auth) is sent — that's how we
	// detect which session is the current one.
	mux.handle("GET", "/api/auth/sessions", requireAuth(d, listSessionsHandler))
	mux.handle("POST", "/api/auth/sessions/revoke-others", requireAuth(d, revokeOtherSessionsHandler))
	mux.handle("POST", "/api/auth/sessions/:jti/revoke", requireAuth(d, revokeSessionHandler))

	mux.handle("GET", "/api/models", requireAuth(d, listModelsHandler))
	mux.handle("GET", "/api/image-models", requireAuth(d, listImageModelsHandler))
	mux.handle("GET", "/api/embedding-models", requireAuth(d, listEmbeddingModelsHandler))
	mux.handle("GET", "/api/skills", requireAuth(d, listSkillsPublicHandler))
	mux.handle("GET", "/api/model-tags", requireAuth(d, listModelTagsPublic))
	// §4.20 Image styles — enabled catalog for the composer style picker (hidden
	// prompt stripped). Image generation itself reuses the chat message endpoint.
	mux.handle("GET", "/api/image/styles", requireAuth(d, listImageStylesPublic))
	// §4.20 the signed-in user's own generated-image gallery.
	mux.handle("GET", "/api/me/images", requireAuth(d, listMyImages))
	mux.handle("GET", "/api/user-groups", requireAuth(d, listUserGroupsPublic))
	mux.handle("POST", "/api/audio/transcriptions", requireAuth(d, transcribeAudioHandler))
	// Which STT provider is active (gpt = record-then-transcribe, volcano = live
	// streaming), so the composer picks the right mic flow.
	mux.handle("GET", "/api/audio/capabilities", requireAuth(d, audioCapabilitiesHandler))
	// Live Volcano (火山引擎 豆包) ASR relay: browser streams 16 kHz PCM over this
	// WebSocket, we proxy it to Volcano and stream transcripts back.
	mux.handle("GET", "/api/audio/stream", requireAuth(d, audioStreamHandler))

	// Workspaces (§workspaces) — collaborative spaces. Join is invite-link-only;
	// the token resolver + join are rate-limited per IP (uniform 404 on unknown
	// tokens) so the 192-bit token space can't be swept.
	mux.handle("GET", "/api/workspaces", requireAuth(d, listWorkspacesHandler))
	mux.handle("POST", "/api/workspaces", requireAuth(d, createWorkspaceHandler))
	mux.handle("DELETE", "/api/workspaces/:id", requireAuth(d, deleteWorkspaceHandler))
	mux.handle("GET", "/api/workspaces/:id/members", requireAuth(d, workspaceMembersHandler))
	mux.handle("DELETE", "/api/workspaces/:id/members/:uid", requireAuth(d, kickWorkspaceMemberHandler))
	mux.handle("POST", "/api/workspaces/:id/leave", requireAuth(d, leaveWorkspaceHandler))
	mux.handle("POST", "/api/workspaces/:id/invite/rotate", requireAuth(d, rotateWorkspaceInviteHandler))
	mux.handle("GET", "/api/workspaces/join/:token", rateLimitedIP(d, "ws_join", rlWorkspaceJoinMax, rlWorkspaceJoinWindow, requireAuth(d, workspaceInviteInfoHandler)))
	mux.handle("POST", "/api/workspaces/join/:token", rateLimitedIP(d, "ws_join", rlWorkspaceJoinMax, rlWorkspaceJoinWindow, requireAuth(d, joinWorkspaceHandler)))

	mux.handle("GET", "/api/projects", requireAuth(d, listProjectsHandler))
	mux.handle("POST", "/api/projects", requireAuth(d, createProjectHandler))
	mux.handle("GET", "/api/projects/:id", requireAuth(d, getProjectHandler))
	mux.handle("PATCH", "/api/projects/:id", requireAuth(d, updateProjectHandler))
	mux.handle("DELETE", "/api/projects/:id", requireAuth(d, deleteProjectHandler))
	mux.handle("GET", "/api/projects/:id/documents", requireAuth(d, listProjectDocsHandler))
	mux.handle("POST", "/api/projects/:id/documents", requireAuth(d, uploadProjectDocHandler))
	mux.handle("DELETE", "/api/projects/:id/documents/:docId", requireAuth(d, deleteProjectDocHandler))
	mux.handle("PATCH", "/api/projects/:id/documents/:docId", requireAuth(d, renameProjectDocHandler))

	mux.handle("GET", "/api/search", requireAuth(d, searchHandler))
	mux.handle("GET", "/api/conversations", requireAuth(d, listConversationsHandler))
	mux.handle("POST", "/api/conversations", requireAuth(d, createConversationHandler))
	mux.handle("POST", "/api/conversations/import", requireAuth(d, importConversationsHandler))
	mux.handle("GET", "/api/conversations/:id", requireAuth(d, getConversationHandler))
	mux.handle("PATCH", "/api/conversations/:id", requireAuth(d, updateConversationHandler))
	mux.handle("DELETE", "/api/conversations/:id", requireAuth(d, deleteConversationHandler))
	mux.handle("GET", "/api/conversations/:id/messages", requireAuth(d, requireReqSig(listMessagesHandler)))
	mux.handle("POST", "/api/conversations/:id/messages", requireAuth(d, postMessageHandler))
	mux.handle("GET", "/api/conversations/:id/messages/:msgId/stream", requireAuth(d, streamMessageHandler))
	mux.handle("PATCH", "/api/conversations/:id/messages/:msgId", requireAuth(d, editMessageHandler))
	mux.handle("DELETE", "/api/conversations/:id/messages/:msgId", requireAuth(d, deleteMessageHandler))
	mux.handle("POST", "/api/conversations/:id/messages/:msgId/feedback", requireAuth(d, feedbackMessageHandler))
	mux.handle("POST", "/api/conversations/:id/stop", requireAuth(d, stopHandler))
	mux.handle("POST", "/api/conversations/:id/regenerate", requireAuth(d, regenerateHandler))
	mux.handle("PATCH", "/api/conversations/:id/active-leaf", requireAuth(d, setActiveLeafHandler))
	mux.handle("POST", "/api/conversations/:id/fork", requireAuth(d, forkConversationHandler))
	mux.handle("GET", "/api/conversations/:id/inline-threads", requireAuth(d, listInlineThreadsHandler))
	mux.handle("POST", "/api/conversations/:id/inline-threads", requireAuth(d, createInlineThreadHandler))
	mux.handle("GET", "/api/conversations/:id/documents", requireAuth(d, listConversationDocsHandler))
	mux.handle("POST", "/api/conversations/:id/documents/:docId/retry", requireAuth(d, retryConversationDocumentHandler))
	mux.handle("POST", "/api/conversations/:id/documents/:docId/promote", requireAuth(d, promoteDocumentHandler))
	// Conversation files drawer (§ conversation files): the authoritative set of
	// files the conversation references. Upload reuses POST /api/files; remove
	// detaches + drops the RAG doc.
	mux.handle("GET", "/api/conversations/:id/files", requireAuth(d, listConversationFilesHandler))
	mux.handle("DELETE", "/api/conversations/:id/files/:fileId", requireAuth(d, deleteConversationFileHandler))
	mux.handle("GET", "/api/conversations/:id/share", requireAuth(d, getShareHandler))
	mux.handle("POST", "/api/conversations/:id/share", requireAuth(d, createShareHandler))
	mux.handle("DELETE", "/api/conversations/:id/share", requireAuth(d, deleteShareHandler))
	mux.handle("POST", "/api/shared/:token/clone", requireAuth(d, cloneSharedConversationHandler))

	mux.handle("POST", "/api/files", requireAuth(d, uploadFileHandler))
	mux.handle("GET", "/api/files/:id", requireAuth(d, downloadFileHandler))
	// User files page (§ user files page): inventory, meter, delete, preview.
	mux.handle("GET", "/api/me/files", requireAuth(d, listMyFilesHandler))
	mux.handle("POST", "/api/me/files/delete", requireAuth(d, deleteMyFilesHandler))
	mux.handle("GET", "/api/me/files/content", requireAuth(d, myFileContentHandler))
	mux.handle("GET", "/api/me/storage", requireAuth(d, myStorageHandler))
	mux.handle("GET", "/api/artifacts/:id", requireAuth(d, downloadArtifactHandler))

	mux.handle("GET", "/api/kbs", requireAuth(d, listKBsHandler))
	mux.handle("POST", "/api/kbs", requireAuth(d, createKBHandler))
	mux.handle("DELETE", "/api/kbs/:id", requireAuth(d, deleteKBHandler))
	mux.handle("POST", "/api/kbs/:id/documents", requireAuth(d, uploadKBDocHandler))
	mux.handle("GET", "/api/kbs/:id/documents", requireAuth(d, listKBDocsHandler))
	mux.handle("DELETE", "/api/kbs/:id/documents/:docId", requireAuth(d, deleteKBDocHandler))

	// Admin endpoints.
	mux.handle("GET", "/api/admin/channels", requireAdmin(d, listChannelsAdmin))
	mux.handle("POST", "/api/admin/channels", requireAdmin(d, createChannelAdmin))
	mux.handle("PATCH", "/api/admin/channels/reorder", requireAdmin(d, reorderChannelsAdmin))
	mux.handle("PATCH", "/api/admin/channels/:id", requireAdmin(d, updateChannelAdmin))
	mux.handle("DELETE", "/api/admin/channels/:id", requireAdmin(d, deleteChannelAdmin))
	mux.handle("GET", "/api/admin/models", requireAdmin(d, listModelsAdmin))
	mux.handle("POST", "/api/admin/models", requireAdmin(d, createModelAdmin))
	// Must precede the /:id PATCH — both are 4-segment PATCH routes and the mux
	// picks the first match, so /reorder would otherwise hit updateModelAdmin.
	mux.handle("PATCH", "/api/admin/models/reorder", requireAdmin(d, reorderModelsAdmin))
	mux.handle("PATCH", "/api/admin/models/:id", requireAdmin(d, updateModelAdmin))
	mux.handle("DELETE", "/api/admin/models/:id", requireAdmin(d, deleteModelAdmin))
	mux.handle("PUT", "/api/admin/models/:id/skills", requireAdmin(d, setModelSkillsAdmin))
	mux.handle("PUT", "/api/admin/models/:id/fast", requireAdmin(d, setFastModelAdmin))
	// Model tags (§ model tags): admin CRUD of the assignable label set.
	mux.handle("GET", "/api/admin/model-tags", requireAdmin(d, listModelTagsAdmin))
	mux.handle("POST", "/api/admin/model-tags", requireAdmin(d, createModelTagAdmin))
	mux.handle("PATCH", "/api/admin/model-tags/:id", requireAdmin(d, updateModelTagAdmin))
	mux.handle("DELETE", "/api/admin/model-tags/:id", requireAdmin(d, deleteModelTagAdmin))
	// §4.20 Image styles — admin CRUD (full row incl. hidden_prompt). Example
	// images are uploaded via the existing POST /api/admin/icons.
	mux.handle("GET", "/api/admin/image-styles", requireAdmin(d, listImageStylesAdmin))
	mux.handle("POST", "/api/admin/image-styles", requireAdmin(d, createImageStyleAdmin))
	mux.handle("PATCH", "/api/admin/image-styles/:id", requireAdmin(d, updateImageStyleAdmin))
	mux.handle("DELETE", "/api/admin/image-styles/:id", requireAdmin(d, deleteImageStyleAdmin))
	mux.handle("GET", "/api/admin/models/:id/quotas", requireAdmin(d, listModelQuotasAdmin))
	mux.handle("PUT", "/api/admin/models/:id/quotas", requireAdmin(d, setModelQuotasAdmin))
	mux.handle("GET", "/api/admin/user-groups", requireAdmin(d, listUserGroupsAdmin))
	mux.handle("POST", "/api/admin/user-groups", requireAdmin(d, createUserGroupAdmin))
	mux.handle("PATCH", "/api/admin/user-groups/reorder", requireAdmin(d, reorderUserGroupsAdmin))
	mux.handle("PATCH", "/api/admin/user-groups/:id", requireAdmin(d, updateUserGroupAdmin))
	mux.handle("DELETE", "/api/admin/user-groups/:id", requireAdmin(d, deleteUserGroupAdmin))
	mux.handle("POST", "/api/admin/users/:id/group", requireAdmin(d, setUserGroupAdmin))
	mux.handle("POST", "/api/admin/users/:id/credits", requireAdmin(d, setUserCreditsAdmin))
	mux.handle("GET", "/api/admin/skills", requireAdmin(d, listSkillsAdmin))
	mux.handle("POST", "/api/admin/skills", requireAdmin(d, createSkillAdmin))
	mux.handle("POST", "/api/admin/skills/assets", requireAdmin(d, uploadSkillAssetAdmin))
	mux.handle("PATCH", "/api/admin/skills/:id", requireAdmin(d, updateSkillAdmin))
	mux.handle("DELETE", "/api/admin/skills/:id", requireAdmin(d, deleteSkillAdmin))
	mux.handle("GET", "/api/admin/users", requireAdmin(d, listUsersAdmin))
	mux.handle("POST", "/api/admin/users", requireAdmin(d, createUserAdmin))
	mux.handle("PATCH", "/api/admin/users/reorder", requireAdmin(d, reorderUsersAdmin))
	mux.handle("DELETE", "/api/admin/users/:id", requireAdmin(d, deleteUserAdmin))
	mux.handle("POST", "/api/admin/users/:id/ban", requireAdmin(d, banUserAdmin))
	mux.handle("POST", "/api/admin/users/:id/unban", requireAdmin(d, unbanUserAdmin))
	mux.handle("POST", "/api/admin/users/:id/password", requireAdmin(d, setUserPasswordAdmin))
	mux.handle("POST", "/api/admin/users/:id/role", requireAdmin(d, setUserRoleAdmin))
	mux.handle("POST", "/api/admin/users/:id/2fa/disable", requireAdmin(d, adminDisableTwofaHandler))
	// User drill-down for support / abuse triage. The list endpoint returns
	// the user's conversations; the thread endpoint returns the full message
	// timeline of one conversation (including assistant/tool turns) so an
	// admin can verify a report without needing to log in as the user.
	mux.handle("GET", "/api/admin/users/:id/conversations", requireAdmin(d, listUserConversationsAdmin))
	mux.handle("GET", "/api/admin/users/:id/projects", requireAdmin(d, listUserProjectsAdmin))
	// §4.20/§8.1 admin drill-down: a user's generated-image gallery.
	mux.handle("GET", "/api/admin/users/:id/images", requireAdmin(d, listUserImagesAdmin))
	mux.handle("GET", "/api/admin/users/:id/kbs", requireAdmin(d, listUserKBsAdmin))
	mux.handle("GET", "/api/admin/kbs/:id/documents", requireAdmin(d, listKBDocumentsAdmin))
	mux.handle("GET", "/api/admin/conversations/:id", requireAdmin(d, getConversationAdmin))
	mux.handle("GET", "/api/admin/conversations/:id/sandbox", requireAdmin(d, sandboxFilesAdmin))
	mux.handle("GET", "/api/admin/conversations/:id/sandbox/file", requireAdmin(d, sandboxFileGetAdmin))
	mux.handle("DELETE", "/api/admin/conversations/:id/sandbox", requireAdmin(d, sandboxClearAdmin))
	mux.handle("GET", "/api/admin/conversations/:id/messages", requireAdmin(d, listConversationMessagesAdmin))
	// Workspaces admin triage (§workspaces 管理端).
	mux.handle("GET", "/api/admin/workspaces", requireAdmin(d, adminListWorkspacesHandler))
	mux.handle("GET", "/api/admin/workspaces/:id", requireAdmin(d, adminWorkspaceDetailHandler))
	mux.handle("DELETE", "/api/admin/workspaces/:id", requireAdmin(d, adminDeleteWorkspaceHandler))
	mux.handle("DELETE", "/api/admin/conversations/:id", requireAdmin(d, deleteConversationAdmin))
	mux.handle("GET", "/api/admin/usage", requireAdmin(d, usageReportAdmin))
	mux.handle("DELETE", "/api/admin/usage", requireAdmin(d, usageDeleteFilteredAdmin))
	mux.handle("DELETE", "/api/admin/usage/:id", requireAdmin(d, usageDeleteOneAdmin))
	mux.handle("GET", "/api/admin/analytics", requireAdmin(d, analyticsAdmin))
	mux.handle("GET", "/api/admin/users/deletions", requireAdmin(d, listUserDeletionsAdmin))
	mux.handle("GET", "/api/admin/files", requireAdmin(d, listFilesAdmin))
	mux.handle("POST", "/api/admin/files/delete", requireAdmin(d, deleteFilesAdmin))
	mux.handle("GET", "/api/admin/files/content", requireAdmin(d, adminFileContentHandler))
	mux.handle("GET", "/api/admin/oauth-providers", requireAdmin(d, listOAuthProvidersAdmin))
	mux.handle("POST", "/api/admin/oauth-providers", requireAdmin(d, createOAuthProviderAdmin))
	mux.handle("PATCH", "/api/admin/oauth-providers/:id", requireAdmin(d, updateOAuthProviderAdmin))
	mux.handle("DELETE", "/api/admin/oauth-providers/:id", requireAdmin(d, deleteOAuthProviderAdmin))
	mux.handle("GET", "/api/admin/settings", requireAdmin(d, adminSettingsGet))
	mux.handle("PATCH", "/api/admin/settings", requireAdmin(d, adminSettingsSet))
	// Database backup / migration (§ admin → data migration). Export streams a
	// logical, engine-neutral archive; import replaces ALL data from one.
	mux.handle("GET", "/api/admin/backup/export", requireAdmin(d, exportBackupAdmin))
	mux.handle("POST", "/api/admin/backup/export-jobs", requireAdmin(d, startBackupExportAdmin))
	mux.handle("GET", "/api/admin/backup/export-jobs", requireAdmin(d, listBackupExportsAdmin))
	mux.handle("GET", "/api/admin/backup/archives/:name", requireAdmin(d, downloadBackupArchiveAdmin))
	mux.handle("POST", "/api/admin/backup/import", requireAdmin(d, importBackupAdmin))
	// Configuration migration. Exports/imports admin configuration tables and
	// admin assets (icons / skill-assets), deliberately leaving users,
	// conversations, user uploads, KBs, sessions, workspaces, and logs untouched.
	mux.handle("GET", "/api/admin/config/export", requireAdmin(d, exportConfigAdmin))
	mux.handle("POST", "/api/admin/config/import", requireAdmin(d, importConfigAdmin))
	// Vector index maintenance: checks DB chunks against Qdrant and can rebuild
	// missing/empty vector points asynchronously.
	mux.handle("GET", "/api/admin/vectors/jobs", requireAdmin(d, listVectorMaintenanceAdmin))
	mux.handle("POST", "/api/admin/vectors/check", requireAdmin(d, startVectorCheckAdmin))
	mux.handle("POST", "/api/admin/vectors/rebuild-missing", requireAdmin(d, startVectorRebuildAdmin))
	// Redeem codes (§ redeem codes). Admin lists/creates/patches/deletes;
	// individual codes can be revoked (enabled=false) without losing audit.
	mux.handle("GET", "/api/admin/redeem-codes", requireAdmin(d, listRedeemCodesAdmin))
	mux.handle("POST", "/api/admin/redeem-codes", requireAdmin(d, createRedeemCodeAdmin))
	mux.handle("PATCH", "/api/admin/redeem-codes/:id", requireAdmin(d, updateRedeemCodeAdmin))
	mux.handle("DELETE", "/api/admin/redeem-codes/:id", requireAdmin(d, deleteRedeemCodeAdmin))
	mux.handle("GET", "/api/admin/redeem-codes/:id/redemptions", requireAdmin(d, listRedeemCodeRedemptionsAdmin))
	mux.handle("DELETE", "/api/admin/redeem-batches/:name", requireAdmin(d, deleteRedeemBatchAdmin))
	// Icon upload — admin-only mint. The stored URL lands in models.icon so the
	// picker can render the image.
	mux.handle("POST", "/api/admin/icons", requireAdmin(d, uploadIconAdmin))
	// Model icons render in <img> tags (model picker, chat) for every user, so
	// they're served publicly: gating them behind requireAuth meant the
	// cookie-auth <img> request 401'd once the access token expired — and an
	// <img> can't trigger the client's token refresh — so the icon went blank
	// "after a while" until a full reload. Filenames are random hex with
	// validated image content, so there's nothing sensitive to protect.
	mux.handle("GET", "/api/icons/:filename", wrap(d, serveIcon))

	// CORS wrapper around the API. When STATIC_DIR is set, the same process also
	// serves the built SPA (single-container deploy) — front + back share one
	// origin, so there's no cross-origin and any domain the server answers on
	// works without configuring PUBLIC_ORIGIN/ALLOWED_ORIGINS. Panic recovery
	// surrounds the application; the response logger sits outside it so the 500
	// written for a recovered panic is included in the common non-2xx log (§ FIX-7).
	var handler http.Handler = corsMiddleware(d.Config.AllowedOrigins, mux)
	if d.Config.StaticDir != "" {
		handler = spaHandler(d.Config.StaticDir, handler)
	}
	return errorResponseLoggingMiddleware(d.Logger, recoverMiddleware(handler))
}

// spaHandler serves the built SPA from dir and routes /api/* to the API. Any
// path that doesn't resolve to a real file falls back to index.html so the
// client-side router (deep links, refreshes) keeps working. Fingerprinted build
// assets under /assets/ get a long immutable cache; index.html stays fresh.
func spaHandler(dir string, api http.Handler) http.Handler {
	indexPath := filepath.Join(dir, "index.html")
	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		// path.Clean collapses any ../ so the join can't escape dir.
		rel := path.Clean("/" + r.URL.Path)
		fp := filepath.Join(dir, filepath.FromSlash(rel))
		if info, err := os.Stat(fp); err == nil && !info.IsDir() {
			if strings.HasPrefix(rel, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			// §23 version probe: must never be cached anywhere (browser, proxy,
			// CDN) or open tabs keep seeing the old version and never refresh.
			if rel == "/version.json" {
				w.Header().Set("Cache-Control", "no-store")
			}
			// A cached stale index.html after a deploy would make the version
			// check arm→reload→still-stale (loop breaker caps it, but fix the
			// cause too). The SPA-fallback branch below already sends this.
			if rel == "/index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback — serve index.html for unknown (client-routed) paths.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, indexPath)
	})
}

// recoverMiddleware catches any handler panic, logs the stack trace, and
// returns a 500 to the client instead of crashing the process (§ FIX-7).
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered", "panic", rec, "stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware enables credentials so the frontend's fetch with
// `credentials: "include"` works. The browser refuses `*` when credentials
// are sent, so we echo the request's Origin if it's in the allow list.
func corsMiddleware(allowed []string, next http.Handler) http.Handler {
	allowSet := map[string]bool{}
	for _, o := range allowed {
		allowSet[strings.TrimRight(o, "/")] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		origin = strings.TrimRight(origin, "/")
		if origin != "" && allowSet[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			// Must list every custom request header the client actually sends, or
			// a cross-origin preflight fails. The signed-request headers
			// (x-req-ts/nonce/token, see verifyReqToken middleware) are on every
			// authenticated call — omitting them breaks all cross-origin API use
			// (i.e. serving the app on a domain other than the API's origin).
			w.Header().Set("Access-Control-Allow-Headers", "content-type, authorization, x-req-ts, x-req-nonce, x-req-token, x-device-id")
			w.Header().Set("Access-Control-Expose-Headers", "Retry-After")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,PUT,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Max-Age", strconv.Itoa(int(corsPreflightMaxAge.Seconds())))
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
