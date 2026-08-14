package tools

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

type runtimePolicyTool struct{ calls int }

func (t *runtimePolicyTool) Name() string        { return "runtime_policy_tool" }
func (t *runtimePolicyTool) Description() string { return "runtime policy test" }
func (t *runtimePolicyTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t *runtimePolicyTool) Execute(context.Context, []byte, *llm.ToolContext) (string, []llm.Citation, error) {
	t.calls++
	return "allowed", nil, nil
}

func TestRegistryRunRejectsToolOutsideResolvedModelPolicy(t *testing.T) {
	registry := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))
	_, _, err := registry.Run(context.Background(), "aivory_web_search", []byte(`{"query":"test"}`), &llm.ToolContext{BuiltinTools: map[string]bool{}})
	if err == nil || !strings.Contains(err.Error(), "not enabled for this model") {
		t.Fatalf("disallowed registry call error = %v", err)
	}
}

func TestRegistryUsesAivoryNameForLocalWebSearch(t *testing.T) {
	registry := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))
	found := false
	for _, definition := range registry.List("") {
		switch definition.Name {
		case "aivory_web_search":
			found = true
		case "web_search":
			t.Fatal("local registry reused the provider-hosted web_search name")
		}
	}
	if !found {
		t.Fatal("local registry did not expose aivory_web_search")
	}
}

func TestRegistryRunRechecksCurrentGroupAndGlobalPolicy(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	permissions := store.DefaultUserGroupPermissions()
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_groups(id,name,permissions) VALUES('runtime-tools','Runtime tools',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES('runtime-user','runtime@example.test','h','user','active','runtime-tools')`); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	tool := &runtimePolicyTool{}
	registry.Register(tool)
	tc := &llm.ToolContext{
		UserID: "runtime-user",
		BuiltinTools: map[string]bool{
			"runtime_policy_tool": true,
		},
	}
	if output, _, err := registry.Run(ctx, tool.Name(), nil, tc); err != nil || output != "allowed" || tool.calls != 1 {
		t.Fatalf("initial allowed call output=%q calls=%d err=%v", output, tool.calls, err)
	}

	permissions.Tools = store.ResourceAccessPolicy{Mode: store.ResourceAccessNone}
	raw, _ = json.Marshal(permissions)
	if _, err := db.ExecContext(ctx, `UPDATE user_groups SET permissions=? WHERE id='runtime-tools'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Run(ctx, tool.Name(), nil, tc); err == nil || !strings.Contains(err.Error(), "no longer allowed") {
		t.Fatalf("group-revoked call error=%v", err)
	}
	if tool.calls != 1 {
		t.Fatalf("group-revoked call reached tool: calls=%d", tool.calls)
	}

	permissions.Tools = store.ResourceAccessPolicy{Mode: store.ResourceAccessAll}
	raw, _ = json.Marshal(permissions)
	if _, err := db.ExecContext(ctx, `UPDATE user_groups SET permissions=? WHERE id='runtime-tools'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "disabled_tools", []string{tool.Name()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Run(ctx, tool.Name(), nil, tc); err == nil || !strings.Contains(err.Error(), "disabled by the administrator") {
		t.Fatalf("globally-disabled call error=%v", err)
	}
	if tool.calls != 1 {
		t.Fatalf("globally-disabled call reached tool: calls=%d", tool.calls)
	}
}
