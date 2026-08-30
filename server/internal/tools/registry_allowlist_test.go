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

func TestRegistryWithholdsPythonUntilSandboxIsConfigured(t *testing.T) {
	withoutSandbox := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))
	for _, definition := range withoutSandbox.List("") {
		if definition.Name == "python_execute" {
			t.Fatal("python_execute was advertised without a configured sandbox")
		}
	}
	foundRegistered := false
	for _, definition := range withoutSandbox.ListRegistered() {
		if definition.Name == "python_execute" {
			foundRegistered = true
		}
	}
	if !foundRegistered {
		t.Fatal("admin registry list did not retain unavailable python_execute")
	}

	withSandbox := NewRegistry(nil, config.Config{SandboxBaseURL: "http://sandbox.internal"}, log.New(io.Discard, "", 0))
	for _, definition := range withSandbox.List("") {
		if definition.Name == "python_execute" {
			return
		}
	}
	t.Fatal("python_execute was not advertised with a configured sandbox")
}

func TestRegistryWithholdsImageGenerationUntilImageModelIsConfigured(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))

	listed := func(definitions []llm.ToolDef, name string) bool {
		for _, definition := range definitions {
			if definition.Name == name {
				return true
			}
		}
		return false
	}
	if listed(registry.List(""), "image_generate") {
		t.Fatal("image_generate was advertised without an enabled image model")
	}
	if !listed(registry.ListRegistered(), "image_generate") {
		t.Fatal("admin registry list did not retain unavailable image_generate")
	}

	channel, err := store.CreateChannel(ctx, db, "Image runtime", "openai", "images", "https://images.example.test/v1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "image", RequestID: "image-runtime", Label: "Image runtime", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !listed(registry.List(""), "image_generate") {
		t.Fatal("image_generate was not advertised after configuring an enabled image model")
	}

	if _, err := db.ExecContext(ctx, `UPDATE models SET enabled=0 WHERE id=?`, model.ID); err != nil {
		t.Fatal(err)
	}
	if listed(registry.List(""), "image_generate") {
		t.Fatal("image_generate remained advertised after disabling the last image model")
	}
	_, _, err = registry.Run(ctx, "image_generate", []byte(`{"action":"generate","prompt":"test"}`), &llm.ToolContext{
		BuiltinTools: map[string]bool{"image_generate": true},
	})
	if err == nil || !strings.Contains(err.Error(), "no image model is configured") {
		t.Fatalf("runtime image model recheck error = %v", err)
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
