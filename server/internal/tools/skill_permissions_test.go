package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func TestUseSkillHonorsAdministratorSkillCeiling(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	mustExec(`INSERT INTO channels(id,name,type) VALUES('skill-channel','Channel','openai')`)
	mustExec(`INSERT INTO models(id,channel_id,request_id,label) VALUES('skill-model','skill-channel','model','Model')`)
	mustExec(`INSERT INTO skills(id,name,description,instructions,assets,enabled) VALUES
		('allowed-skill','Allowed skill','desc','ALLOWED-INSTRUCTIONS','[]',1),
		('denied-skill','Denied skill','desc','DENIED-INSTRUCTIONS','[]',1)`)
	mustExec(`INSERT INTO model_skills(model_id,skill_id) VALUES
		('skill-model','allowed-skill'),('skill-model','denied-skill')`)

	tool := &useSkillTool{db: db}
	tc := &llm.ToolContext{
		ModelID:       "skill-model",
		AdminSkillIDs: map[string]bool{"allowed-skill": true},
	}
	input, _ := json.Marshal(skillInput{Name: "Allowed skill"})
	output, _, err := tool.Execute(ctx, input, tc)
	if err != nil || !strings.Contains(output, "ALLOWED-INSTRUCTIONS") {
		t.Fatalf("allowed skill failed: output=%q err=%v", output, err)
	}

	input, _ = json.Marshal(skillInput{Name: "Denied skill"})
	output, _, err = tool.Execute(ctx, input, tc)
	if err != nil {
		t.Fatalf("denied skill lookup returned an execution error: %v", err)
	}
	if strings.Contains(output, "DENIED-INSTRUCTIONS") || !strings.Contains(output, "Skill not found") {
		t.Fatalf("denied skill crossed the execution boundary: %q", output)
	}

	if !tc.AllowsAdminSkill("allowed-skill") || tc.AllowsAdminSkill("denied-skill") {
		t.Fatal("test fixture did not retain the selected skill")
	}
}

func TestUseSkillRechecksCurrentGroupSkillSelection(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	permissions := store.DefaultUserGroupPermissions()
	permissions.Skills = store.ResourceAccessPolicy{
		Mode: store.ResourceAccessSelected,
		IDs:  []string{"allowed-skill", "revoked-skill"},
	}
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO user_groups(id,name,permissions) VALUES('skill-group','Skills',?)`, []any{string(raw)}},
		{`INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES('skill-user','skill-user@example.test','h','user','active','skill-group')`, nil},
		{`INSERT INTO channels(id,name,type) VALUES('skill-channel','Channel','openai')`, nil},
		{`INSERT INTO models(id,channel_id,request_id,label) VALUES('skill-model','skill-channel','model','Model')`, nil},
		{`INSERT INTO skills(id,name,description,instructions,assets,enabled) VALUES
			('allowed-skill','Allowed skill','desc','ALLOWED-INSTRUCTIONS','[]',1),
			('revoked-skill','Revoked skill','desc','REVOKED-INSTRUCTIONS','[]',1)`, nil},
		{`INSERT INTO model_skills(model_id,skill_id) VALUES
			('skill-model','allowed-skill'),('skill-model','revoked-skill')`, nil},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("exec %q: %v", statement.query, err)
		}
	}

	tool := &useSkillTool{db: db}
	tc := &llm.ToolContext{
		UserID: "skill-user", ModelID: "skill-model",
		AdminSkillIDs: map[string]bool{"allowed-skill": true, "revoked-skill": true},
	}
	docGenInput, _ := json.Marshal(skillInput{Name: llm.DocGenSkillName})
	docGenOutput, _, err := tool.Execute(ctx, docGenInput, tc)
	if err != nil {
		t.Fatalf("selected-policy built-in skill lookup error=%v", err)
	}
	if strings.Contains(docGenOutput, llm.DocGenRecipes) || !strings.Contains(docGenOutput, "Skill not found") {
		t.Fatalf("selected policy exposed code-defined skill without a catalog ID: %q", docGenOutput)
	}

	input, _ := json.Marshal(skillInput{Name: "Revoked skill"})
	output, _, err := tool.Execute(ctx, input, tc)
	if err != nil || !strings.Contains(output, "REVOKED-INSTRUCTIONS") {
		t.Fatalf("initial skill lookup output=%q err=%v", output, err)
	}

	permissions.Skills.IDs = []string{"allowed-skill"}
	raw, _ = json.Marshal(permissions)
	if _, err := db.ExecContext(ctx, `UPDATE user_groups SET permissions=? WHERE id='skill-group'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	output, _, err = tool.Execute(ctx, input, tc)
	if err != nil {
		t.Fatalf("revoked skill lookup error=%v", err)
	}
	if strings.Contains(output, "REVOKED-INSTRUCTIONS") || !strings.Contains(output, "Skill not found") {
		t.Fatalf("stale skill snapshot crossed current group policy: %q", output)
	}

	permissions.Skills = store.ResourceAccessPolicy{Mode: store.ResourceAccessAll}
	raw, _ = json.Marshal(permissions)
	if _, err := db.ExecContext(ctx, `UPDATE user_groups SET permissions=? WHERE id='skill-group'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	tc.AdminSkillIDs = nil
	docGenOutput, _, err = tool.Execute(ctx, docGenInput, tc)
	if err != nil || !strings.Contains(docGenOutput, llm.DocGenRecipes) {
		t.Fatalf("all policy should expose built-in document skill: output=%q err=%v", docGenOutput, err)
	}
}
