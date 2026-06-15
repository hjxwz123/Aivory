// Package api wires the HTTP API. The router is plain net/http with a tiny
// custom mux to keep the dependency surface small. Each handler module owns
// the endpoints for one feature area (auth, conversations, projects, files,
// kbs, admin, etc.).
package api

import (
	"database/sql"
	"log"
	"net/http"
	"strings"
	"time"

	"aurelia/server/internal/auth"
	"aurelia/server/internal/cache"
	"aurelia/server/internal/config"
	"aurelia/server/internal/llm"
	"aurelia/server/internal/mail"
	"aurelia/server/internal/queue"
	"aurelia/server/internal/rag"
	"aurelia/server/internal/tools"
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

// NewRouter returns the fully-wired http.Handler.
func NewRouter(d Deps) http.Handler {
	mux := newMux()

	// Public endpoints.
	// §8 brute-force defence: tight IP-scoped rate limit on auth surfaces.
	// 10 requests per IP per 60s — enough for retries on typos, not enough
	// to enumerate passwords / spam refresh.
	mux.handle("POST", "/api/auth/register", rateLimitedIP(d, "auth", 5, 60*time.Second, wrap(d, registerHandler)))
	mux.handle("POST", "/api/auth/login", rateLimitedIP(d, "auth", 10, 60*time.Second, wrap(d, loginHandler)))
	mux.handle("POST", "/api/auth/login/2fa", rateLimitedIP(d, "auth", 10, 60*time.Second, wrap(d, login2faHandler)))
	mux.handle("POST", "/api/auth/logout", rateLimitedIP(d, "auth", 30, 60*time.Second, wrap(d, logoutHandler)))
	mux.handle("POST", "/api/auth/refresh", rateLimitedIP(d, "auth", 30, 60*time.Second, wrap(d, refreshHandler)))
	mux.handle("POST", "/api/auth/verify-email", rateLimitedIP(d, "auth", 20, 60*time.Second, wrap(d, verifyEmailHandler)))
	mux.handle("POST", "/api/auth/send-code", rateLimitedIP(d, "auth", 3, 60*time.Second, wrap(d, sendCodeHandler)))
	mux.handle("POST", "/api/auth/forgot-password", rateLimitedIP(d, "auth", 3, 60*time.Second, wrap(d, forgotPasswordHandler)))
	mux.handle("POST", "/api/auth/reset-password", rateLimitedIP(d, "auth", 5, 60*time.Second, wrap(d, resetPasswordHandler)))
	mux.handle("GET", "/api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.handle("GET", "/api/public/signup-open", wrap(d, signupOpenHandler))
	mux.handle("GET", "/api/public/oauth-providers", wrap(d, oauthProvidersPublicHandler))
	// Public read-only conversation share (token in the path; no auth). Rate
	// limited (§D1) so the token space can't be swept even though it's now 192-bit.
	mux.handle("GET", "/api/public/shared/:token", rateLimitedIP(d, "share", 60, 60*time.Second, wrap(d, publicSharedHandler)))

	// OAuth / social login. /start is a top-level browser navigation; /callback
	// is hit by the provider redirect (GET) or Apple's form_post (POST).
	mux.handle("GET", "/api/auth/oauth/:id/start", rateLimitedIP(d, "auth", 20, 60*time.Second, wrap(d, oauthStartHandler)))
	mux.handle("GET", "/api/auth/oauth/:id/callback", rateLimitedIP(d, "auth", 20, 60*time.Second, wrap(d, oauthCallbackHandler)))
	mux.handle("POST", "/api/auth/oauth/:id/callback", rateLimitedIP(d, "auth", 20, 60*time.Second, wrap(d, oauthCallbackHandler)))

	// Authenticated endpoints.
	mux.handle("GET", "/api/me", requireAuth(d, meHandler))
	mux.handle("PATCH", "/api/me", requireAuth(d, updateMeHandler))
	mux.handle("DELETE", "/api/me", requireAuth(d, deleteMeHandler))
	mux.handle("PATCH", "/api/me/password", requireAuth(d, changePasswordHandler))
	mux.handle("POST", "/api/me/password/set", requireAuth(d, setPasswordHandler))
	mux.handle("GET", "/api/me/usage", requireAuth(d, meUsageHandler))
	mux.handle("GET", "/api/me/settings", requireAuth(d, meSettingsHandler))
	mux.handle("PATCH", "/api/me/settings", requireAuth(d, updateMeSettingsHandler))
	mux.handle("GET", "/api/me/upload-policy", requireAuth(d, meUploadPolicyHandler))
	mux.handle("GET", "/api/announcement", requireAuth(d, announcementHandler))
	mux.handle("POST", "/api/me/2fa/setup", requireAuth(d, twofaSetupHandler))
	mux.handle("POST", "/api/me/2fa/enable", requireAuth(d, twofaEnableHandler))
	mux.handle("POST", "/api/me/2fa/disable", requireAuth(d, twofaDisableHandler))
	mux.handle("GET", "/api/me/memories", requireAuth(d, listMemoriesHandler))
	mux.handle("POST", "/api/me/memories", requireAuth(d, createMemoryHandler))
	mux.handle("PATCH", "/api/me/memories/:id", requireAuth(d, updateMemoryHandler))
	mux.handle("DELETE", "/api/me/memories/:id", requireAuth(d, deleteMemoryHandler))
	// Redeem a code → grants the user a configured user-group for a configured
	// duration (§ redeem codes). Tight rate limit so a stolen code can't be
	// brute-force-typed by an attacker who knows the alphabet.
	mux.handle("POST", "/api/me/redeem", rateLimitedIP(d, "redeem", 10, 60*time.Second, requireAuth(d, redeemCodeHandler)))

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
	mux.handle("GET", "/api/user-groups", requireAuth(d, listUserGroupsPublic))
	mux.handle("POST", "/api/audio/transcriptions", requireAuth(d, transcribeAudioHandler))

	mux.handle("GET", "/api/projects", requireAuth(d, listProjectsHandler))
	mux.handle("POST", "/api/projects", requireAuth(d, createProjectHandler))
	mux.handle("GET", "/api/projects/:id", requireAuth(d, getProjectHandler))
	mux.handle("PATCH", "/api/projects/:id", requireAuth(d, updateProjectHandler))
	mux.handle("DELETE", "/api/projects/:id", requireAuth(d, deleteProjectHandler))
	mux.handle("GET", "/api/projects/:id/documents", requireAuth(d, listProjectDocsHandler))
	mux.handle("POST", "/api/projects/:id/documents", requireAuth(d, uploadProjectDocHandler))
	mux.handle("DELETE", "/api/projects/:id/documents/:docId", requireAuth(d, deleteProjectDocHandler))
	mux.handle("PATCH", "/api/projects/:id/documents/:docId", requireAuth(d, renameProjectDocHandler))

	mux.handle("GET", "/api/conversations", requireAuth(d, listConversationsHandler))
	mux.handle("POST", "/api/conversations", requireAuth(d, createConversationHandler))
	mux.handle("GET", "/api/conversations/:id", requireAuth(d, getConversationHandler))
	mux.handle("PATCH", "/api/conversations/:id", requireAuth(d, updateConversationHandler))
	mux.handle("DELETE", "/api/conversations/:id", requireAuth(d, deleteConversationHandler))
	mux.handle("GET", "/api/conversations/:id/messages", requireAuth(d, listMessagesHandler))
	mux.handle("POST", "/api/conversations/:id/messages", requireAuth(d, postMessageHandler))
	mux.handle("PATCH", "/api/conversations/:id/messages/:msgId", requireAuth(d, editMessageHandler))
	mux.handle("POST", "/api/conversations/:id/messages/:msgId/feedback", requireAuth(d, feedbackMessageHandler))
	mux.handle("POST", "/api/conversations/:id/stop", requireAuth(d, stopHandler))
	mux.handle("POST", "/api/conversations/:id/regenerate", requireAuth(d, regenerateHandler))
	mux.handle("PATCH", "/api/conversations/:id/active-leaf", requireAuth(d, setActiveLeafHandler))
	mux.handle("POST", "/api/conversations/:id/fork", requireAuth(d, forkConversationHandler))
	mux.handle("GET", "/api/conversations/:id/documents", requireAuth(d, listConversationDocsHandler))
	mux.handle("POST", "/api/conversations/:id/documents/:docId/promote", requireAuth(d, promoteDocumentHandler))
	mux.handle("GET", "/api/conversations/:id/share", requireAuth(d, getShareHandler))
	mux.handle("POST", "/api/conversations/:id/share", requireAuth(d, createShareHandler))
	mux.handle("DELETE", "/api/conversations/:id/share", requireAuth(d, deleteShareHandler))

	mux.handle("POST", "/api/files", requireAuth(d, uploadFileHandler))
	mux.handle("GET", "/api/files/:id", requireAuth(d, downloadFileHandler))
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
	mux.handle("GET", "/api/admin/models/:id/quotas", requireAdmin(d, listModelQuotasAdmin))
	mux.handle("PUT", "/api/admin/models/:id/quotas", requireAdmin(d, setModelQuotasAdmin))
	mux.handle("GET", "/api/admin/user-groups", requireAdmin(d, listUserGroupsAdmin))
	mux.handle("POST", "/api/admin/user-groups", requireAdmin(d, createUserGroupAdmin))
	mux.handle("PATCH", "/api/admin/user-groups/:id", requireAdmin(d, updateUserGroupAdmin))
	mux.handle("DELETE", "/api/admin/user-groups/:id", requireAdmin(d, deleteUserGroupAdmin))
	mux.handle("POST", "/api/admin/users/:id/group", requireAdmin(d, setUserGroupAdmin))
	mux.handle("GET", "/api/admin/skills", requireAdmin(d, listSkillsAdmin))
	mux.handle("POST", "/api/admin/skills", requireAdmin(d, createSkillAdmin))
	mux.handle("PATCH", "/api/admin/skills/:id", requireAdmin(d, updateSkillAdmin))
	mux.handle("DELETE", "/api/admin/skills/:id", requireAdmin(d, deleteSkillAdmin))
	mux.handle("GET", "/api/admin/users", requireAdmin(d, listUsersAdmin))
	mux.handle("POST", "/api/admin/users", requireAdmin(d, createUserAdmin))
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
	mux.handle("GET", "/api/admin/conversations/:id", requireAdmin(d, getConversationAdmin))
	mux.handle("GET", "/api/admin/conversations/:id/messages", requireAdmin(d, listConversationMessagesAdmin))
	mux.handle("DELETE", "/api/admin/conversations/:id", requireAdmin(d, deleteConversationAdmin))
	mux.handle("GET", "/api/admin/usage", requireAdmin(d, usageReportAdmin))
	mux.handle("GET", "/api/admin/analytics", requireAdmin(d, analyticsAdmin))
	mux.handle("GET", "/api/admin/oauth-providers", requireAdmin(d, listOAuthProvidersAdmin))
	mux.handle("POST", "/api/admin/oauth-providers", requireAdmin(d, createOAuthProviderAdmin))
	mux.handle("PATCH", "/api/admin/oauth-providers/:id", requireAdmin(d, updateOAuthProviderAdmin))
	mux.handle("DELETE", "/api/admin/oauth-providers/:id", requireAdmin(d, deleteOAuthProviderAdmin))
	mux.handle("GET", "/api/admin/settings", requireAdmin(d, adminSettingsGet))
	mux.handle("PATCH", "/api/admin/settings", requireAdmin(d, adminSettingsSet))
	// Redeem codes (§ redeem codes). Admin lists/creates/patches/deletes;
	// individual codes can be revoked (enabled=false) without losing audit.
	mux.handle("GET", "/api/admin/redeem-codes", requireAdmin(d, listRedeemCodesAdmin))
	mux.handle("POST", "/api/admin/redeem-codes", requireAdmin(d, createRedeemCodeAdmin))
	mux.handle("PATCH", "/api/admin/redeem-codes/:id", requireAdmin(d, updateRedeemCodeAdmin))
	mux.handle("DELETE", "/api/admin/redeem-codes/:id", requireAdmin(d, deleteRedeemCodeAdmin))
	mux.handle("GET", "/api/admin/redeem-codes/:id/redemptions", requireAdmin(d, listRedeemCodeRedemptionsAdmin))
	mux.handle("DELETE", "/api/admin/redeem-batches/:name", requireAdmin(d, deleteRedeemBatchAdmin))
	// Icon upload — admin-only mint, any authenticated user can read. The
	// stored URL lands in models.icon so the picker can render the image.
	mux.handle("POST", "/api/admin/icons", requireAdmin(d, uploadIconAdmin))
	mux.handle("GET", "/api/icons/:filename", requireAuth(d, serveIcon))

	// CORS wrapper.
	return corsMiddleware(d.Config.AllowedOrigins, mux)
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
			w.Header().Set("Access-Control-Allow-Headers", "content-type, authorization, x-aurelia-csrf")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,PUT,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
