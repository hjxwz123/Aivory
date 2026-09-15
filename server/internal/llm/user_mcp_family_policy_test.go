package llm

import (
	"aivory/server/internal/store"
	"testing"
)

func TestUserMCPFamilySelectionStaysWithinIntersectedCeilings(t *testing.T) {
	group := store.DefaultUserGroupPermissions()
	group.Tools = store.ResourceAccessPolicy{Mode: store.ResourceAccessSelected, IDs: []string{"usermcp:*"}}
	policy := groupToolAccessPolicy(group)
	for _, id := range []string{"usermcp:mine", "usermcp:teammate"} {
		if !store.ResourcePolicyAllows(group.Tools, id) || !policy.Allows(id) {
			t.Fatalf("family did not admit %s", id)
		}
	}
	for _, id := range []string{"mcp:official", "builtin:web_search", "hosted:web_search", "usermcp:"} {
		if policy.Allows(id) {
			t.Fatalf("family broadened access to %s", id)
		}
	}
	narrow := groupToolAccessPolicy(store.DefaultUserGroupPermissions())
	narrow.Mode, narrow.IDs = store.ResourceAccessSelected, []string{"usermcp:mine"}
	for _, pair := range [][2]*ToolAccessPolicy{{policy, narrow}, {narrow, policy}} {
		merged := intersectToolAccessPolicies(pair[0], pair[1])
		if !merged.Allows("usermcp:mine") || merged.Allows("usermcp:teammate") {
			t.Fatalf("invalid family intersection: %+v", merged)
		}
	}
	workspace := store.DefaultWorkspacePolicy("ws")
	workspace.AllowMCP = false
	if intersectToolAccessPolicies(policy, workspaceToolAccessPolicy(workspace)).Allows("usermcp:mine") {
		t.Fatal("family bypassed workspace MCP shutdown")
	}
	group.Tools.Mode = store.ResourceAccessNone
	if intersectToolAccessPolicies(policy, groupToolAccessPolicy(group)).Allows("usermcp:mine") {
		t.Fatal("family bypassed group shutdown")
	}
}
