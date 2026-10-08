package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestSkillCommandsExposePermittedCatalogWithoutCopy(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "skill-commands.db"))
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash) VALUES('u1','commands@example.test','h')`); err != nil {
		t.Fatal(err)
	}
	skill, err := store.CreateSkill(t.Context(), db, store.Skill{Name: "visual-test", Enabled: true, DisplayDescription: "Visuals", Description: "TRIGGER_SECRET", Instructions: "BODY_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db}
	list := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		listSkillCommandsHandler(d, rec, libraryRequest(t, http.MethodGet, path, "", "u1"))
		return rec
	}
	rec := list("/api/me/skill-commands")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "catalog:"+skill.ID) || strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatalf("commands=%d %s", rec.Code, rec.Body.String())
	}
	rows, _ := store.ListUserSkillsScoped(t.Context(), db, "u1", "")
	if len(rows) != 0 {
		t.Fatal("listing created a personal copy")
	}
	selected, ids, err := resolvePermittedUserSkillSelection(t.Context(), db, "u1", "", []string{"catalog:" + skill.ID}, true, store.ResourceAccessPolicy{Mode: store.ResourceAccessAll})
	if err != nil || len(ids) != 1 || selected[0].Instructions != "BODY_SECRET" {
		t.Fatalf("selection=%+v ids=%v err=%v", selected, ids, err)
	}
	copy, err := store.CreateUserSkill(t.Context(), db, store.UserSkill{UserID: "u1", Name: "my-visual", SourceSkillID: skill.ID, Instructions: "EDITED"})
	if err != nil {
		t.Fatal(err)
	}
	rec = list("/api/me/skill-commands")
	if strings.Contains(rec.Body.String(), "catalog:"+skill.ID) || !strings.Contains(rec.Body.String(), copy.ID) || strings.Contains(rec.Body.String(), "EDITED") {
		t.Fatalf("copy duplicate/metadata=%s", rec.Body.String())
	}
	if rec = list("/api/me/skill-commands?workspace_id=not-a-member"); rec.Code == 200 {
		t.Fatal("workspace scope bypassed")
	}
	permissions := store.DefaultUserGroupPermissions()
	permissions.Skills = store.ResourceAccessPolicy{Mode: store.ResourceAccessNone}
	raw, _ := json.Marshal(permissions)
	if _, err := db.Exec(`INSERT INTO user_groups(id,name,permissions) VALUES('cmd-group','Command group',?); UPDATE users SET group_id='cmd-group' WHERE id='u1'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	rec = list("/api/me/skill-commands")
	if rec.Code != 200 || rec.Body.String() != "[]\n" {
		t.Fatalf("denied catalog=%d %s", rec.Code, rec.Body.String())
	}
	if _, _, err := resolvePermittedUserSkillSelection(t.Context(), db, "u1", "", []string{"catalog:" + skill.ID}, true, permissions.Skills); err == nil {
		t.Fatal("catalog policy bypass")
	}
}
