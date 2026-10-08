package store

import (
	"errors"
	"strings"
	"testing"
)

func TestBuiltinSkillSeedPreservesAdministratorChanges(t *testing.T) {
	db, ctx := openLibraryTestDB(t)
	if err := seedBuiltinSkills(db); err != nil {
		t.Fatal(err)
	}
	skill, err := GetSkill(ctx, db, GenerativeUISkillID)
	if err != nil || !skill.Enabled || !strings.Contains(skill.Instructions, "aivory-ui") {
		t.Fatalf("skill=%+v err=%v", skill, err)
	}
	if _, err := db.Exec(`UPDATE skills SET instructions='custom',enabled=0 WHERE id=?`, GenerativeUISkillID); err != nil {
		t.Fatal(err)
	}
	if err := seedBuiltinSkills(db); err != nil {
		t.Fatal(err)
	}
	skill, _ = GetSkill(ctx, db, GenerativeUISkillID)
	if skill.Enabled || skill.Instructions != "custom" {
		t.Fatal("seed overwrote administrator choices")
	}
}

func TestCatalogCommandsResolveWithoutLibraryCopyAndDeduplicate(t *testing.T) {
	db, ctx := openLibraryTestDB(t)
	if err := seedBuiltinSkills(db); err != nil {
		t.Fatal(err)
	}
	id := CatalogSkillCommandPrefix + GenerativeUISkillID
	selected, ids, err := ResolveUserSkillSelectionScoped(ctx, db, "u1", "", []string{id, id}, true)
	if err != nil || len(selected) != 1 || len(ids) != 1 || selected[0].SourceSkillID != GenerativeUISkillID {
		t.Fatalf("selection=%+v ids=%v err=%v", selected, ids, err)
	}
	copy, err := CreateUserSkill(ctx, db, UserSkill{UserID: "u1", Name: "my-visuals", Instructions: "EDITED", SourceSkillID: GenerativeUISkillID})
	if err != nil {
		t.Fatal(err)
	}
	selected, ids, err = ResolveUserSkillSelectionScoped(ctx, db, "u1", "", []string{id, copy.ID}, true)
	if err != nil || len(selected) != 1 || selected[0].Instructions != "EDITED" || ids[0] != copy.ID {
		t.Fatalf("selection=%+v ids=%v err=%v", selected, ids, err)
	}
	if _, err := db.Exec(`UPDATE skills SET enabled=0 WHERE id=?`, GenerativeUISkillID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveUserSkillSelectionScoped(ctx, db, "u1", "", []string{id}, true); !errors.Is(err, ErrInvalidUserSkillSelection) {
		t.Fatalf("disabled selection err=%v", err)
	}
	selected, _, err = ResolveUserSkillSelectionScoped(ctx, db, "u1", "", []string{id}, false)
	if err != nil || len(selected) != 0 {
		t.Fatal("regeneration retained disabled direct reference")
	}
}
