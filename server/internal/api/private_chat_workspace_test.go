package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func TestPrivateChatWorkspaceAndGroupIntersection(t *testing.T) {
	for _, tc := range []struct {
		name, role, memberRole string
		group, workspace       bool
		want                   int
	}{
		{"member allowed", "user", "member", true, true, 200},
		{"member group denied", "user", "member", false, true, 403},
		{"member workspace denied", "user", "member", true, false, 403},
		{"workspace admin group denied", "user", "admin", false, true, 403},
		{"workspace admin workspace denied", "user", "admin", true, false, 403},
		{"owner allowed", "user", "owner", true, true, 200},
		{"owner group denied", "user", "owner", false, true, 403},
		{"site admin bypasses group", "admin", "owner", false, true, 200},
		{"site admin obeys workspace", "admin", "owner", false, false, 403},
		{"guest denied", "user", "guest", true, true, 403},
		{"nonmember denied", "user", "", true, true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := seedImageCapabilityFixture(t)
			if err := store.SetSetting(fx.deps.DB, "credits_per_usd", 100.0); err != nil {
				t.Fatal(err)
			}
			provider := &privateAPIProvider{}
			registry := llm.NewRegistry(nil)
			registry.Register(provider)
			fx.deps.Orchestrator = llm.NewOrchestrator(fx.deps.DB, registry, nil, nil, nil, nil, nil, nil, nil)
			owner, err := store.CreateUser(t.Context(), fx.deps.DB, "owner@outside.example", "Owner", "hash")
			if err != nil {
				t.Fatal(err)
			}
			if tc.memberRole == "owner" {
				owner = fx.user
			}
			ws, err := store.CreateWorkspace(t.Context(), fx.deps.DB, owner.ID, "Private chat workspace")
			if err != nil {
				t.Fatal(err)
			}
			if tc.memberRole != "owner" && tc.memberRole != "" {
				mustExec(t, fx.deps.DB, `INSERT INTO workspace_members(workspace_id,user_id,role,can_private_conversations) VALUES(?,?,?,0)`, ws.ID, fx.user.ID, tc.memberRole)
			}
			p := store.DefaultUserGroupPermissions()
			p.AllowPrivateChat = tc.group
			setIntersectionGroup(t, fx.deps, fx.user.ID, p)
			if err := store.SetPermanentCredits(t.Context(), fx.deps.DB, fx.user.ID, 100); err != nil {
				t.Fatal(err)
			}
			fx.user.Role = tc.role
			mustExec(t, fx.deps.DB, `UPDATE users SET role=? WHERE id=?`, tc.role, fx.user.ID)
			// Exercise the public PATCH field mapping as well as persistence.
			rec := httptest.NewRecorder()
			body, _ := json.Marshal(map[string]bool{"allow_private_chat": tc.workspace})
			updateWorkspacePolicyHandler(fx.deps, rec, userGroupPermissionRequest(http.MethodPatch, "/", owner, map[string]string{"id": ws.ID}, string(body)))
			if rec.Code != 200 {
				t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
			}
			rec = httptest.NewRecorder()
			body, _ = json.Marshal(privateChatRequest{WorkspaceID: ws.ID, ModelID: "m_plain", Messages: []privateChatMessage{{Role: "user", Text: "private prompt"}}})
			privateChatHandler(fx.deps, rec, userGroupPermissionRequest(http.MethodPost, "/api/private-chat", fx.user, nil, string(body)))
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
			calls := 0
			if tc.want == 200 {
				calls = 1
			}
			if provider.calls != calls {
				t.Fatalf("provider calls=%d want=%d response=%s", provider.calls, calls, rec.Body.String())
			}
			if tc.want == 200 && !strings.Contains(rec.Body.String(), "private response") {
				t.Fatalf("response=%s", rec.Body.String())
			}
			var count int
			if err := fx.deps.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("private chat saved messages: %d %v", count, err)
			}
			if err := fx.deps.DB.QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE workspace_id=? AND conversation_id IS NULL AND request_body=''`, ws.ID).Scan(&count); err != nil || count != calls {
				t.Fatalf("workspace usage metadata=%d want=%d err=%v", count, calls, err)
			}
		})
	}
}

func TestPrivateChatHonorsWorkspaceModelUploadAndCreditCeilings(t *testing.T) {
	for _, reason := range []string{"model", "images", "credits"} {
		t.Run(reason, func(t *testing.T) {
			fx := seedImageCapabilityFixture(t)
			provider := &privateAPIProvider{}
			registry := llm.NewRegistry(nil)
			registry.Register(provider)
			fx.deps.Orchestrator = llm.NewOrchestrator(fx.deps.DB, registry, nil, nil, nil, nil, nil, nil, nil)
			ws, err := store.CreateWorkspace(t.Context(), fx.deps.DB, fx.user.ID, "Ceiling")
			if err != nil {
				t.Fatal(err)
			}
			patch := store.WorkspacePolicyPatch{}
			body := privateChatRequest{WorkspaceID: ws.ID, ModelID: "m_vision", Messages: []privateChatMessage{{Role: "user", Text: "private prompt"}}}
			switch reason {
			case "model":
				ids := []string{"m_plain"}
				patch.AllowedModelIDs = &ids
			case "images":
				allow := false
				patch.AllowFileUpload = &allow
				body.Messages[0].Images = []privateChatImage{{MimeType: "image/png", Data: "unused-before-validation"}}
			case "credits":
				limit := 1.0
				patch.MemberMonthlyCreditLimit = &limit
				if err := store.LogUsageAnalytics(t.Context(), fx.deps.DB, store.UsageLog{UserID: fx.user.ID, WorkspaceID: ws.ID, MessageID: "private_previous", ModelID: "m_plain", Credits: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.UpdateWorkspacePolicy(t.Context(), fx.deps.DB, ws.ID, fx.user.ID, patch); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(body)
			rec := httptest.NewRecorder()
			privateChatHandler(fx.deps, rec, userGroupPermissionRequest(http.MethodPost, "/api/private-chat", fx.user, nil, string(raw)))
			if rec.Code != http.StatusForbidden || provider.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, provider.calls, rec.Body.String())
			}
		})
	}
}

type privateCancellationProvider struct{ started chan struct{} }

func (*privateCancellationProvider) ID() string { return "openai" }
func (p *privateCancellationProvider) Stream(ctx context.Context, _ llm.UnifiedChatRequest, _ llm.ToolRunner, _ func(llm.SseEvent)) (*llm.UnifiedResult, error) {
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPrivateChatWorkspaceShutdownCancelsInFlightRequest(t *testing.T) {
	fx := seedImageCapabilityFixture(t)
	provider := &privateCancellationProvider{started: make(chan struct{})}
	registry := llm.NewRegistry(nil)
	registry.Register(provider)
	fx.deps.Orchestrator = llm.NewOrchestrator(fx.deps.DB, registry, nil, nil, nil, nil, nil, nil, nil)
	ws, err := store.CreateWorkspace(t.Context(), fx.deps.DB, fx.user.ID, "Private cancellation")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(privateChatRequest{WorkspaceID: ws.ID, ModelID: "m_plain", Messages: []privateChatMessage{{Role: "user", Text: "private prompt"}}})
	req := userGroupPermissionRequest(http.MethodPost, "/api/private-chat", fx.user, nil, string(body))
	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); privateChatHandler(fx.deps, httptest.NewRecorder(), req.WithContext(ctx)) }()
	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	rec := httptest.NewRecorder()
	updateWorkspacePolicyHandler(fx.deps, rec, userGroupPermissionRequest(http.MethodPatch, "/", fx.user, map[string]string{"id": ws.ID}, `{"allow_private_chat":false}`))
	if rec.Code != 200 {
		t.Fatalf("disable=%d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("workspace shutdown did not cancel private request")
	}
}

func TestDomainLockedPrivateChatCannotOmitOrForgeWorkspace(t *testing.T) {
	fx := seedImageCapabilityFixture(t)
	if err := store.SetSetting(fx.deps.DB, "credits_per_usd", 100.0); err != nil {
		t.Fatal(err)
	}
	registry := llm.NewRegistry(nil)
	provider := &privateAPIProvider{}
	registry.Register(provider)
	fx.deps.Orchestrator = llm.NewOrchestrator(fx.deps.DB, registry, nil, nil, nil, nil, nil, nil, nil)
	ws, err := store.CreateWorkspace(t.Context(), fx.deps.DB, fx.user.ID, "Company")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRegistrationDomain(t.Context(), fx.deps.DB, store.RegistrationDomain{Domain: "company.example", WorkspaceID: ws.ID, LockPersonal: true, Enabled: true}, true); err != nil {
		t.Fatal(err)
	}
	member, err := store.CreateUser(t.Context(), fx.deps.DB, "member@company.example", "Member", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPermanentCredits(t.Context(), fx.deps.DB, member.ID, 100); err != nil {
		t.Fatal(err)
	}
	for _, allow := range []bool{false, true} {
		if _, err := store.UpdateWorkspacePolicy(t.Context(), fx.deps.DB, ws.ID, fx.user.ID, store.WorkspacePolicyPatch{AllowPrivateChat: &allow}); err != nil {
			t.Fatal(err)
		}
		for _, scope := range []string{"", ws.ID, "foreign-workspace"} {
			body, _ := json.Marshal(privateChatRequest{WorkspaceID: scope, ModelID: "m_plain", Messages: []privateChatMessage{{Role: "user", Text: "private prompt"}}})
			req := userGroupPermissionRequest(http.MethodPost, "/api/private-chat", member, nil, string(body))
			rec := httptest.NewRecorder()
			if !enforceDomainAccess(fx.deps, rec, req, member.ID) {
				privateChatHandler(fx.deps, rec, req)
			}
			want := 403
			if allow && scope != "foreign-workspace" {
				want = 200
			}
			if rec.Code != want {
				t.Fatalf("allow=%v scope=%q status=%d want=%d body=%s", allow, scope, rec.Code, want, rec.Body.String())
			}
		}
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls=%d", provider.calls)
	}
}
