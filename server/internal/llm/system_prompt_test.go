package llm

import (
	"strings"
	"testing"
)

// TestComposeSystemPromptIdentity locks in §identity: the assistant identifies as
// the model's admin-configured display name, never the hardcoded product name.
func TestComposeSystemPromptIdentity(t *testing.T) {
	s := composeSystemPrompt(systemPromptOpts{ModelLabel: "GPT 5.5"})
	if !strings.Contains(s, "You are GPT 5.5") {
		t.Errorf("identity should be the model label; prompt = %q", s)
	}
	if strings.Contains(s, "Aivory") {
		t.Error("system prompt must not contain the hardcoded product name")
	}

	// A custom model system prompt is layered ON TOP of the built-in identity.
	s2 := composeSystemPrompt(systemPromptOpts{ModelLabel: "GPT 5.5", ModelSystem: "Be terse."})
	if !strings.Contains(s2, "You are GPT 5.5") || !strings.Contains(s2, "Be terse.") {
		t.Errorf("identity + custom prompt should both appear; got %q", s2)
	}

	// No label → generic fallback, still no product name.
	s3 := composeSystemPrompt(systemPromptOpts{})
	if !strings.Contains(s3, "an AI assistant") || strings.Contains(s3, "Aivory") {
		t.Errorf("empty label should fall back generically; got %q", s3)
	}
}

// TestComposeSystemPromptDocGenSkill locks in §4.5.1 progressive disclosure:
// a model that can call use_skill gets a one-line skills-index entry instead
// of the multi-KB recipes; a model that can't still gets them inline.
func TestComposeSystemPromptDocGenSkill(t *testing.T) {
	// use_skill available → recipes NOT inlined, built-in index entry present.
	s := composeSystemPrompt(systemPromptOpts{
		ToolMode:           "native",
		ToolNames:          []string{"python_execute", "use_skill"},
		SkillToolAvailable: true,
		SkillsAllowed:      true,
	})
	if strings.Contains(s, "## Document-generation recipes") {
		t.Error("recipes must not be inlined when use_skill can load them on demand")
	}
	if !strings.Contains(s, "- "+DocGenSkillName+": ") {
		t.Errorf("skills index should advertise the built-in %s skill; got %q", DocGenSkillName, s)
	}

	// No use_skill (prompt mode) → recipes inlined, no index entry.
	s2 := composeSystemPrompt(systemPromptOpts{
		ToolMode:      "prompt",
		ToolNames:     []string{"python_execute"},
		SkillsAllowed: true,
	})
	if !strings.Contains(s2, "## Document-generation recipes") {
		t.Error("recipes must stay inline when the model cannot call use_skill")
	}
	if strings.Contains(s2, "## Skills available") {
		t.Error("no skills index without use_skill")
	}

	// No python_execute → neither inline recipes nor the index entry.
	s3 := composeSystemPrompt(systemPromptOpts{
		ToolMode:           "native",
		ToolNames:          []string{"web_search", "use_skill"},
		SkillToolAvailable: true,
		SkillsAllowed:      true,
	})
	if strings.Contains(s3, DocGenSkillName) || strings.Contains(s3, "## Document-generation recipes") {
		t.Error("document-generation must not appear without python_execute")
	}

	// Admin skill with the same name shadows the built-in — exactly one entry.
	s4 := composeSystemPrompt(systemPromptOpts{
		ToolMode:           "native",
		ToolNames:          []string{"python_execute", "use_skill"},
		SkillToolAvailable: true,
		SkillsAllowed:      true,
		Skills:             []SkillIndex{{Name: "Document-Generation", When: "admin override"}},
	})
	if n := strings.Count(strings.ToLower(s4), "document-generation"); n != 1 {
		t.Errorf("admin skill must shadow the built-in exactly once, got %d occurrences in %q", n, s4)
	}
	if !strings.Contains(s4, "admin override") {
		t.Error("admin-defined description should win over the built-in")
	}

	// An explicit use_skill policy denial blocks both assigned skills and the
	// built-in DocGen recipes, even while python_execute remains available.
	denied := composeSystemPrompt(systemPromptOpts{
		ToolMode:  "prompt",
		ToolNames: []string{"python_execute"},
		SkillsFull: []SkillFull{{
			Name: "must-not-appear", Instructions: "PRIVATE-SKILL-INSTRUCTIONS",
		}},
		SkillsAllowed: false,
	})
	for _, forbidden := range []string{"## Document-generation recipes", "must-not-appear", "PRIVATE-SKILL-INSTRUCTIONS"} {
		if strings.Contains(denied, forbidden) {
			t.Errorf("use_skill denial leaked %q into prompt: %s", forbidden, denied)
		}
	}
}
