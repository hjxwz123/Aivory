package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/sandbox"
	"aivory/server/internal/store"
)

type stagedSandboxFile struct {
	path string
	data []byte
}

type recordingSandbox struct {
	resetCalls int
	resetErr   error
	putFiles   []stagedSandboxFile
	execCalls  int
	execResult *sandbox.Result
}

func (s *recordingSandbox) Enabled() bool { return true }
func (s *recordingSandbox) NewSession(context.Context, string) (string, error) {
	return "sandbox-1", nil
}
func (s *recordingSandbox) Exec(context.Context, string, string) (*sandbox.Result, error) {
	s.execCalls++
	if s.execResult != nil {
		return s.execResult, nil
	}
	return &sandbox.Result{}, nil
}
func (s *recordingSandbox) PutFile(_ context.Context, _ string, path string, data []byte) error {
	s.putFiles = append(s.putFiles, stagedSandboxFile{path: path, data: append([]byte(nil), data...)})
	return nil
}
func (s *recordingSandbox) ResetInputs(context.Context, string) error {
	s.resetCalls++
	return s.resetErr
}
func (s *recordingSandbox) GetFile(context.Context, string, string) ([]byte, error) {
	return nil, nil
}
func (s *recordingSandbox) ListFiles(context.Context, string) ([]sandbox.SandboxFile, error) {
	return nil, nil
}
func (s *recordingSandbox) Release(context.Context, string) error { return nil }
func (s *recordingSandbox) ReleaseDiscard(context.Context, string, string) error {
	return nil
}
func (s *recordingSandbox) PruneArchives(context.Context, time.Duration) (int, error) {
	return 0, nil
}

func TestSandboxImageInputClassificationUsesMetadataExtensionAndBytes(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	avif := append([]byte{0, 0, 0, 24}, []byte("ftypavif")...)
	cases := []struct {
		name     string
		filename string
		mime     string
		kind     string
		data     []byte
		want     bool
	}{
		{name: "kind", filename: "payload.bin", mime: "application/octet-stream", kind: "image", want: true},
		{name: "mime", filename: "payload.bin", mime: "image/png; charset=binary", kind: "text", want: true},
		{name: "extension", filename: "photo.HEIC", mime: "application/octet-stream", kind: "text", want: true},
		{name: "forged metadata png bytes", filename: "notes.dat", mime: "text/plain", kind: "text", data: png, want: true},
		{name: "forged metadata avif bytes", filename: "notes.dat", mime: "text/plain", kind: "text", data: avif, want: true},
		{name: "svg text bytes", filename: "notes.txt", mime: "text/plain", kind: "text", data: []byte(`<?xml version="1.0"?><svg viewBox="0 0 1 1"></svg>`), want: true},
		{name: "ordinary csv", filename: "rows.csv", mime: "text/csv", kind: "sheet", data: []byte("a,b\n1,2\n"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSandboxImageInput(tc.filename, tc.mime, tc.kind, tc.data); got != tc.want {
				t.Fatalf("isSandboxImageInput() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPythonExecuteResetsInputsAndStagesOnlyNonImages(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	root := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	paths := map[string]string{
		"csv":           write("rows.csv", []byte("a,b\n1,2\n")),
		"image":         write("photo.png", png),
		"forged":        write("payload.dat", png),
		"imageArtifact": write("generated.png", png),
		"skillText":     write("helper.py", []byte("print('ok')\n")),
		"skillImage":    write("reference.bin", png),
	}

	assets, err := json.Marshal([]map[string]any{
		{"filename": "helper.py", "storage_path": paths["skillText"], "mime_type": "text/x-python", "size_bytes": 12},
		{"filename": "reference.bin", "storage_path": paths["skillImage"], "mime_type": "text/plain", "size_bytes": len(png)},
	})
	if err != nil {
		t.Fatalf("marshal assets: %v", err)
	}
	seed := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,password_hash,name) VALUES('u1','u1@example.com','hash','User')`, nil},
		{`INSERT INTO channels(id,name,type) VALUES('ch1','Channel','openai')`, nil},
		{`INSERT INTO models(id,channel_id,request_id,label) VALUES('m1','ch1','model','Model')`, nil},
		{`INSERT INTO conversations(id,user_id,title,model_id) VALUES('c1','u1','Test','m1')`, nil},
		{`INSERT INTO messages(id,conversation_id,role) VALUES('msg1','c1','assistant')`, nil},
		{`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('f_csv','u1','c1','rows.csv','text/csv',?,?, 'sheet')`, []any{len("a,b\n1,2\n"), paths["csv"]}},
		{`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('f_img','u1','c1','photo.png','image/png',?,?, 'image')`, []any{len(png), paths["image"]}},
		{`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('f_forged','u1','c1','payload.dat','text/plain',?,?, 'text')`, []any{len(png), paths["forged"]}},
		{`INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes) VALUES('art1','msg1','generated.png',?,'image/png',?)`, []any{paths["imageArtifact"], len(png)}},
		{`INSERT INTO skills(id,name,description,instructions,assets,enabled) VALUES('sk1','Data helper','desc','instructions',?,1)`, []any{string(assets)}},
		{`INSERT INTO model_skills(model_id,skill_id) VALUES('m1','sk1')`, nil},
	}
	for _, row := range seed {
		if _, err := db.ExecContext(ctx, row.query, row.args...); err != nil {
			t.Fatalf("seed %q: %v", row.query, err)
		}
	}

	fake := &recordingSandbox{execResult: &sandbox.Result{
		Stdout: "done\n",
		Files:  []sandbox.File{{Name: "plot.png", MimeType: "image/png", Data: png}},
	}}
	var outputArtifact llm.ArtifactRef
	tool := &pythonExecuteTool{sandbox: fake, artifactDir: filepath.Join(root, "artifacts"), logger: log.New(io.Discard, "", 0)}
	_, _, err = tool.Execute(ctx, []byte(`{"code":"print('done')"}`), &llm.ToolContext{
		UserID: "u1", ConvID: "c1", MessageID: "msg1", ModelID: "m1", DB: db,
		BuiltinTools: map[string]bool{"python_execute": true, "use_skill": true},
		OnArtifact:   func(ref llm.ArtifactRef) { outputArtifact = ref },
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.resetCalls != 1 {
		t.Fatalf("ResetInputs calls = %d, want 1", fake.resetCalls)
	}
	if fake.execCalls != 1 {
		t.Fatalf("Exec calls = %d, want 1", fake.execCalls)
	}
	wantPaths := map[string]bool{
		"/workspace/uploads/rows.csv":             true,
		"/workspace/skills/Data helper/helper.py": true,
	}
	if len(fake.putFiles) != len(wantPaths) {
		t.Fatalf("staged paths = %+v, want only non-image data and skill files", fake.putFiles)
	}
	for _, staged := range fake.putFiles {
		if !wantPaths[staged.path] {
			t.Errorf("unexpected staged input %q", staged.path)
		}
		if isSandboxImageInput(staged.path, "", "", staged.data) {
			t.Errorf("image bytes reached PutFile at %q", staged.path)
		}
	}
	if outputArtifact.MimeType != "image/png" {
		t.Fatalf("Python image output was not preserved as an artifact: %+v", outputArtifact)
	}

	// The Python tool itself can remain enabled while use_skill is denied. In
	// that policy, ordinary uploads are still staged but model-bound skill files
	// must not cross into the sandbox.
	deniedFake := &recordingSandbox{execResult: &sandbox.Result{Stdout: "done\n"}}
	deniedTool := &pythonExecuteTool{sandbox: deniedFake, artifactDir: filepath.Join(root, "artifacts"), logger: log.New(io.Discard, "", 0)}
	_, _, err = deniedTool.Execute(ctx, []byte(`{"code":"print('done')"}`), &llm.ToolContext{
		UserID: "u1", ConvID: "c1", MessageID: "msg1", ModelID: "m1", DB: db,
		BuiltinTools: map[string]bool{"python_execute": true},
	})
	if err != nil {
		t.Fatalf("Execute with use_skill denied: %v", err)
	}
	if len(deniedFake.putFiles) != 1 || deniedFake.putFiles[0].path != "/workspace/uploads/rows.csv" {
		t.Fatalf("use_skill denial staged model skill assets: %+v", deniedFake.putFiles)
	}
}

func TestPythonExecuteFailsClosedWhenPersistentInputsCannotBeReset(t *testing.T) {
	fake := &recordingSandbox{resetErr: errors.New("old sidecar")}
	tool := &pythonExecuteTool{sandbox: fake, logger: log.New(io.Discard, "", 0)}
	_, _, err := tool.Execute(context.Background(), []byte(`{"code":"print(1)"}`), &llm.ToolContext{})
	if err == nil || !strings.Contains(err.Error(), "reset sandbox inputs") {
		t.Fatalf("Execute error = %v, want reset failure", err)
	}
	if fake.execCalls != 0 {
		t.Fatalf("Exec ran %d time(s) despite reset failure", fake.execCalls)
	}
}

func TestFetchImageIsNotAdvertisedAndAlwaysFailsClosed(t *testing.T) {
	registry := NewRegistry(nil, nil, config.Config{}, log.New(io.Discard, "", 0))
	for _, def := range registry.List("") {
		if def.Name == "fetch_image" {
			t.Fatal("fetch_image must not be advertised to models")
		}
	}
	_, _, err := (&fetchImageTool{}).Execute(context.Background(), []byte(`{"url":"https://example.com/photo.png"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "cannot be staged in the sandbox") {
		t.Fatalf("fetch_image error = %v, want hard sandbox boundary", err)
	}
}

func TestImageGenerateReferenceUploadsAreVerifiedAndConversationScoped(t *testing.T) {
	oldCap := fetchRemoteImageDownloadCap
	fetchRemoteImageDownloadCap = 64
	t.Cleanup(func() { fetchRemoteImageDownloadCap = oldCap })

	ctx := context.Background()
	db := openToolsTestDB(t)
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,name) VALUES('u1','u1@example.com','hash','User')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','One')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c2','u1','Two')`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)
	pdf := []byte("%PDF-1.7\nnot an image")
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, file := range []store.File{
		{ID: "same", UserID: "u1", ConversationID: "c1", Filename: "same.bin", MimeType: "text/plain", Kind: "text", SizeBytes: int64(len(png)), StoragePath: write("same.bin", png)},
		{ID: "cross", UserID: "u1", ConversationID: "c2", Filename: "cross.png", MimeType: "image/png", Kind: "image", SizeBytes: int64(len(png)), StoragePath: write("cross.png", png)},
		{ID: "fake", UserID: "u1", ConversationID: "c1", Filename: "fake.png", MimeType: "image/png", Kind: "image", SizeBytes: int64(len(pdf)), StoragePath: write("fake.png", pdf)},
	} {
		if _, err := store.CreateFile(ctx, db, file); err != nil {
			t.Fatal(err)
		}
	}

	tool := &imageGenerateTool{db: db}
	images := tool.loadInputImages(ctx, &llm.ToolContext{DB: db, UserID: "u1", ConvID: "c1"}, []string{"same", "cross", "fake"})
	if len(images) != 1 || images[0].mime != "image/png" || string(images[0].data) != string(png) {
		t.Fatalf("verified reference images = %+v, want only same-conversation PNG bytes", images)
	}
}

func openToolsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "tools.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}
