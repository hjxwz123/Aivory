package llm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

// fakePyRegistry implements llm.ToolRegistry, returning canned python_execute
// output so forcedSandboxRead can be exercised without a live sandbox.
type fakePyRegistry struct {
	out   string
	err   error
	calls int
}

func (f *fakePyRegistry) List(string) []ToolDef { return nil }
func (f *fakePyRegistry) Run(_ context.Context, name string, _ []byte, _ *ToolContext) (string, []Citation, error) {
	if name != "python_execute" {
		return "", nil, nil
	}
	f.calls++
	return f.out, nil, f.err
}

func TestSandboxFilesHaveSheet(t *testing.T) {
	if sandboxFilesHaveSheet([]ProjectFileSummary{{Name: "a.txt", Kind: "text"}, {Name: "b.png", Kind: "image"}}) {
		t.Fatal("no sheet present, should be false")
	}
	if !sandboxFilesHaveSheet([]ProjectFileSummary{{Name: "a.txt", Kind: "text"}, {Name: "data.xlsx", Kind: "sheet"}}) {
		t.Fatal("a sheet is present, should be true")
	}
	if sandboxFilesHaveSheet(nil) {
		t.Fatal("nil should be false")
	}
}

// A no-tools turn with a staged spreadsheet reads it server-side and injects a
// bounded <uploaded-data-preview> block, streaming python_execute progress.
func TestForcedSandboxReadInjectsPreview(t *testing.T) {
	reg := &fakePyRegistry{out: "=== FILE: data.xlsx ===\nshape: 3 rows x 2 cols\ncolumns: a, b\n   a  b\n0  1  2"}
	o := &Orchestrator{tools: reg}
	var events []SseEvent
	text := o.forcedSandboxRead(
		context.Background(),
		RunRequest{UserID: "u1", ConversationID: "c1", ModelID: "m1"},
		&store.Conversation{ID: "c1"},
		func(ev SseEvent) { events = append(events, ev) },
	)
	if !strings.Contains(text, "<uploaded-data-preview>") || !strings.Contains(text, "data.xlsx") || !strings.Contains(text, "shape: 3 rows") {
		t.Fatalf("preview block missing spreadsheet content: %q", text)
	}
	if reg.calls != 1 {
		t.Fatalf("python_execute should run exactly once, got %d", reg.calls)
	}
	var start, result bool
	for _, e := range events {
		switch e.Type {
		case "tool_start":
			start = e.Name == "python_execute"
		case "tool_result":
			result = e.Name == "python_execute" && e.Status == "complete"
		}
	}
	if !start || !result {
		t.Fatalf("missing progress events: start=%v result=%v", start, result)
	}
}

// The preview is capped so a huge sheet can't blow the context budget.
func TestForcedSandboxReadCapsHugePreview(t *testing.T) {
	reg := &fakePyRegistry{out: "=== FILE: big.csv ===\n" + strings.Repeat("x", forcedSandboxReadInjectionCap*2)}
	o := &Orchestrator{tools: reg}
	text := o.forcedSandboxRead(
		context.Background(),
		RunRequest{UserID: "u1", ConversationID: "c1"},
		&store.Conversation{ID: "c1"},
		func(SseEvent) {},
	)
	if !strings.Contains(text, "…(truncated)") {
		t.Fatalf("oversized preview should be truncated: len=%d", len([]rune(text)))
	}
	if len([]rune(text)) > forcedSandboxReadInjectionCap+80 {
		t.Fatalf("preview not capped: %d runes", len([]rune(text)))
	}
}

// No spreadsheets found / sandbox in safe-mode → inject nothing (the marker the
// dump prints, and the tool's safe-mode sentence, both suppress injection).
func TestForcedSandboxReadInjectsNothingWhenEmptyOrUnconfigured(t *testing.T) {
	for _, out := range []string{
		"stdout:\nNO_SPREADSHEET_FILES\n",
		"[python_execute is in safe-mode] Configure a sandbox URL + key in Admin settings to execute real Python.",
		"   ",
	} {
		reg := &fakePyRegistry{out: out}
		o := &Orchestrator{tools: reg}
		text := o.forcedSandboxRead(
			context.Background(),
			RunRequest{UserID: "u1", ConversationID: "c1"},
			&store.Conversation{ID: "c1"},
			func(SseEvent) {},
		)
		if text != "" {
			t.Fatalf("output %q should inject nothing, got %q", out, text)
		}
	}
}

// The forced read must honour the admin `disabled_tools` kill-switch — a disabled
// python_execute must not run through this back door either.
func TestForcedSandboxReadRespectsDisabledTools(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "disabled-py.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.SetSetting(db, "disabled_tools", []string{"python_execute"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	reg := &fakePyRegistry{out: "=== FILE: x.csv ===\nshould never run"}
	o := &Orchestrator{tools: reg, db: db}
	text := o.forcedSandboxRead(
		context.Background(),
		RunRequest{UserID: "u1", ConversationID: "c1"},
		&store.Conversation{ID: "c1"},
		func(SseEvent) {},
	)
	if text != "" {
		t.Fatalf("disabled python_execute must inject nothing, got %q", text)
	}
	if reg.calls != 0 {
		t.Fatalf("python_execute must not be invoked when disabled, calls=%d", reg.calls)
	}
}

// Sanity: the embedded dump snippet is valid enough to matter — it must glob
// /workspace/uploads and cover both the csv and excel read paths.
func TestForcedSpreadsheetDumpCodeShape(t *testing.T) {
	for _, want := range []string{"/workspace/uploads/", "read_csv", "read_excel", "NO_SPREADSHEET_FILES", "head(30)"} {
		if !strings.Contains(forcedSpreadsheetDumpCode, want) {
			t.Fatalf("dump code missing %q", want)
		}
	}
	// Make sure it is passed as a real JSON string field (no accidental escaping trap).
	if _, err := json.Marshal(map[string]any{"code": forcedSpreadsheetDumpCode}); err != nil {
		t.Fatalf("dump code not JSON-encodable: %v", err)
	}
}
