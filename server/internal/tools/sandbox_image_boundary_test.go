package tools

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
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
	resetCalls  int
	resetErr    error
	resetErrors []error
	newSessions int
	putFiles    []stagedSandboxFile
	putErr      error
	execCalls   int
	execResult  *sandbox.Result
}

func (s *recordingSandbox) Enabled() bool { return true }
func (s *recordingSandbox) NewSession(context.Context, string) (string, error) {
	s.newSessions++
	return fmt.Sprintf("sandbox-%d", s.newSessions), nil
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
	return s.putErr
}
func (s *recordingSandbox) ResetInputs(context.Context, string) error {
	s.resetCalls++
	if len(s.resetErrors) > 0 {
		err := s.resetErrors[0]
		s.resetErrors = s.resetErrors[1:]
		return err
	}
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

func TestPythonExecuteResetsInputsAndStagesEveryConversationUpload(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	root := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
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
		"docx":          write("original.docx", []byte("PK\x03\x04docx-original")),
		"pptx":          write("slides.pptx", []byte("PK\x03\x04pptx-original")),
		"pdf":           write("contract.pdf", []byte("%PDF-1.7\npdf-original")),
		"imageArtifact": write(filepath.Join("artifacts", "generated.png"), png),
		"skillText":     write(filepath.Join("skill-assets", "helper.py"), []byte("print('ok')\n")),
		"skillImage":    write(filepath.Join("skill-assets", "reference.bin"), png),
		"deniedSkill":   write(filepath.Join("skill-assets", "denied.py"), []byte("print('denied')\n")),
	}

	assets, err := json.Marshal([]map[string]any{
		{"filename": "helper.py", "storage_path": paths["skillText"], "mime_type": "text/x-python", "size_bytes": 12},
		{"filename": "reference.bin", "storage_path": paths["skillImage"], "mime_type": "text/plain", "size_bytes": len(png)},
	})
	if err != nil {
		t.Fatalf("marshal assets: %v", err)
	}
	deniedAssets, err := json.Marshal([]map[string]any{{
		"filename": "denied.py", "storage_path": paths["deniedSkill"], "mime_type": "text/x-python", "size_bytes": 16,
	}})
	if err != nil {
		t.Fatalf("marshal denied assets: %v", err)
	}
	seed := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,password_hash,name) VALUES('u1','u1@example.com','hash','User')`, nil},
		{`INSERT INTO channels(id,name,type) VALUES('ch1','Channel','openai')`, nil},
		{`INSERT INTO models(id,channel_id,request_id,label) VALUES('m1','ch1','model','Model')`, nil},
		{`INSERT INTO conversations(id,user_id,title,model_id) VALUES('c1','u1','Test','m1')`, nil},
		{`INSERT INTO messages(id,conversation_id,role,author_id,status) VALUES('msg1','c1','assistant','u1','streaming')`, nil},
		{`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('f_csv','u1','c1','rows.csv','text/csv',?,?, 'sheet')`, []any{len("a,b\n1,2\n"), paths["csv"]}},
		{`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('f_img','u1','c1','photo.png','image/png',?,?, 'image')`, []any{len(png), paths["image"]}},
		{`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('f_forged','u1','c1','payload.dat','text/plain',?,?, 'text')`, []any{len(png), paths["forged"]}},
		{`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('f_docx','u1','c1','original.docx','application/vnd.openxmlformats-officedocument.wordprocessingml.document',?,?, 'document')`, []any{len("PK\x03\x04docx-original"), paths["docx"]}},
		{`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('f_pptx','u1','c1','slides.pptx','application/vnd.openxmlformats-officedocument.presentationml.presentation',?,?, 'document')`, []any{len("PK\x03\x04pptx-original"), paths["pptx"]}},
		{`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('f_pdf','u1','c1','contract.pdf','application/pdf',?,?, 'document')`, []any{len("%PDF-1.7\npdf-original"), paths["pdf"]}},
		{`INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes) VALUES('art1','msg1','generated.png',?,'image/png',?)`, []any{paths["imageArtifact"], len(png)}},
		{`INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes,source) VALUES('art_python','msg1','python.png',?,'image/png',?,'python_execute')`, []any{paths["imageArtifact"], len(png)}},
		{`INSERT INTO skills(id,name,description,instructions,assets,enabled) VALUES('sk1','Data helper','desc','instructions',?,1)`, []any{string(assets)}},
		{`INSERT INTO skills(id,name,description,instructions,assets,enabled) VALUES('sk2','Denied helper','desc','denied instructions',?,1)`, []any{string(deniedAssets)}},
		{`INSERT INTO model_skills(model_id,skill_id) VALUES('m1','sk1')`, nil},
		{`INSERT INTO model_skills(model_id,skill_id) VALUES('m1','sk2')`, nil},
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
	tool := &pythonExecuteTool{sandbox: fake, uploadDir: root, artifactDir: filepath.Join(root, "artifacts"), logger: log.New(io.Discard, "", 0)}
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
		"/workspace/uploads/rows.csv":                true,
		"/workspace/uploads/photo.png":               true,
		"/workspace/uploads/payload.dat":             true,
		"/workspace/uploads/original.docx":           true,
		"/workspace/uploads/slides.pptx":             true,
		"/workspace/uploads/contract.pdf":            true,
		"/workspace/uploads/generated-generated.png": true,
		"/workspace/skills/Data helper/helper.py":    true,
		"/workspace/skills/Denied helper/denied.py":  true,
	}
	if len(fake.putFiles) != len(wantPaths) {
		t.Fatalf("staged paths = %+v, want every conversation upload plus eligible generated/skill files", fake.putFiles)
	}
	for _, staged := range fake.putFiles {
		if !wantPaths[staged.path] {
			t.Errorf("unexpected staged input %q", staged.path)
		}
	}
	for path, original := range map[string][]byte{
		"/workspace/uploads/original.docx": []byte("PK\x03\x04docx-original"),
		"/workspace/uploads/slides.pptx":   []byte("PK\x03\x04pptx-original"),
		"/workspace/uploads/contract.pdf":  []byte("%PDF-1.7\npdf-original"),
	} {
		for _, staged := range fake.putFiles {
			if staged.path == path && !bytes.Equal(staged.data, original) {
				t.Errorf("%s was not staged with its original bytes", path)
			}
		}
	}
	if outputArtifact.MimeType != "image/png" {
		t.Fatalf("Python image output was not preserved as an artifact: %+v", outputArtifact)
	}

	// A selected group policy must stage only the permitted model-bound skill.
	selectedFake := &recordingSandbox{execResult: &sandbox.Result{Stdout: "done\n"}}
	selectedTool := &pythonExecuteTool{sandbox: selectedFake, uploadDir: root, artifactDir: filepath.Join(root, "artifacts"), logger: log.New(io.Discard, "", 0)}
	_, _, err = selectedTool.Execute(ctx, []byte(`{"code":"print('done')"}`), &llm.ToolContext{
		UserID: "u1", ConvID: "c1", MessageID: "msg1", ModelID: "m1", DB: db,
		BuiltinTools:  map[string]bool{"python_execute": true, "use_skill": true},
		AdminSkillIDs: map[string]bool{"sk1": true},
	})
	if err != nil {
		t.Fatalf("Execute with selected skill policy: %v", err)
	}
	selectedPaths := map[string]bool{}
	for _, staged := range selectedFake.putFiles {
		selectedPaths[staged.path] = true
	}
	if !selectedPaths["/workspace/skills/Data helper/helper.py"] || selectedPaths["/workspace/skills/Denied helper/denied.py"] {
		t.Fatalf("selected skill policy staged the wrong assets: %+v", selectedFake.putFiles)
	}

	// The Python tool itself can remain enabled while use_skill is denied. In
	// that policy, ordinary uploads are still staged but model-bound skill files
	// must not cross into the sandbox.
	deniedFake := &recordingSandbox{execResult: &sandbox.Result{Stdout: "done\n"}}
	deniedTool := &pythonExecuteTool{sandbox: deniedFake, uploadDir: root, artifactDir: filepath.Join(root, "artifacts"), logger: log.New(io.Discard, "", 0)}
	_, _, err = deniedTool.Execute(ctx, []byte(`{"code":"print('done')"}`), &llm.ToolContext{
		UserID: "u1", ConvID: "c1", MessageID: "msg1", ModelID: "m1", DB: db,
		BuiltinTools: map[string]bool{"python_execute": true},
	})
	if err != nil {
		t.Fatalf("Execute with use_skill denied: %v", err)
	}
	deniedPaths := map[string]bool{}
	for _, staged := range deniedFake.putFiles {
		deniedPaths[staged.path] = true
	}
	if len(deniedPaths) != 7 || !deniedPaths["/workspace/uploads/original.docx"] || !deniedPaths["/workspace/uploads/slides.pptx"] || !deniedPaths["/workspace/uploads/contract.pdf"] || !deniedPaths["/workspace/uploads/generated-generated.png"] {
		t.Fatalf("use_skill denial did not preserve all ordinary uploads: %+v", deniedFake.putFiles)
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

func TestPythonExecuteDoesNotRunWhenConversationUploadCannotBeStaged(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	root := t.TempDir()
	docx := []byte("PK\x03\x04docx-original")
	path := filepath.Join(root, "original.docx")
	if err := os.WriteFile(path, docx, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,name) VALUES('u1','u1@example.com','hash','User')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Test')`,
		`INSERT INTO messages(id,conversation_id,role,author_id,status) VALUES('m1','c1','assistant','u1','streaming')`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('docx','u1','c1','original.docx','application/vnd.openxmlformats-officedocument.wordprocessingml.document',?,?, 'document')`,
		len(docx), path,
	); err != nil {
		t.Fatal(err)
	}

	fake := &recordingSandbox{putErr: errors.New("sidecar rejected upload")}
	tool := &pythonExecuteTool{sandbox: fake, uploadDir: root, logger: log.New(io.Discard, "", 0)}
	_, _, err := tool.Execute(ctx, []byte(`{"code":"print(1)"}`), &llm.ToolContext{DB: db, UserID: "u1", ConvID: "c1", MessageID: "m1"})
	if err == nil || !strings.Contains(err.Error(), `stage conversation upload "original.docx"`) {
		t.Fatalf("Execute error = %v, want conversation upload staging failure", err)
	}
	if fake.execCalls != 0 {
		t.Fatalf("Exec ran %d time(s) despite upload staging failure", fake.execCalls)
	}
}

func TestPythonExecuteRebuildsSessionWhenInitialResetFindsReapedSandbox(t *testing.T) {
	fake := &recordingSandbox{
		resetErrors: []error{errors.New(`sandbox 404: {"detail":"session not found or not running"}`), nil},
		execResult:  &sandbox.Result{Stdout: "ok\n"},
	}
	tool := &pythonExecuteTool{sandbox: fake, logger: log.New(io.Discard, "", 0)}
	output, _, err := tool.Execute(context.Background(), []byte(`{"code":"print('ok')"}`), &llm.ToolContext{})
	if err != nil {
		t.Fatalf("Execute after reset 404: %v", err)
	}
	if output != "stdout:\nok\n\n" {
		t.Fatalf("output=%q, want rebuilt session result", output)
	}
	if fake.newSessions != 2 || fake.resetCalls != 2 || fake.execCalls != 1 {
		t.Fatalf("calls new=%d reset=%d exec=%d, want 2/2/1", fake.newSessions, fake.resetCalls, fake.execCalls)
	}
}

func TestFetchImageIsAdvertisedAndRequiresConversationContext(t *testing.T) {
	registry := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))
	found := false
	for _, def := range registry.List("") {
		if def.Name == "fetch_image" {
			found = true
		}
	}
	if !found {
		t.Fatal("fetch_image was not advertised to models")
	}
	_, _, err := (&fetchImageTool{}).Execute(context.Background(), []byte(`{"url":"https://example.com/photo.png"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("fetch_image error = %v, want sandbox/configuration error", err)
	}
}

type staticImageRoundTripper struct{}

func (staticImageRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(png)),
		Header:     make(http.Header),
	}, nil
}

func TestFetchImageStagesVerifiedDownloadInPersistentDownloadDirectory(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash) VALUES('u_fetch','fetch@example.test','h')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO conversations(id,user_id,title) VALUES('c_fetch','u_fetch','Fetch')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages(id,conversation_id,role,author_id,status) VALUES('m_fetch','c_fetch','assistant','u_fetch','streaming')`); err != nil {
		t.Fatal(err)
	}
	fake := &recordingSandbox{}
	tool := &fetchImageTool{sandbox: fake, client: &http.Client{Transport: staticImageRoundTripper{}}, logger: log.New(io.Discard, "", 0)}
	output, _, err := tool.Execute(ctx, []byte(`{"url":"https://cdn.example.test/photo"}`), &llm.ToolContext{
		DB: db, UserID: "u_fetch", ConvID: "c_fetch", MessageID: "m_fetch",
	})
	if err != nil {
		t.Fatalf("fetch image: %v", err)
	}
	if len(fake.putFiles) != 1 || !strings.HasPrefix(fake.putFiles[0].path, "/workspace/downloads/photo-") || !strings.HasSuffix(fake.putFiles[0].path, ".png") {
		t.Fatalf("staged download = %+v", fake.putFiles)
	}
	if !strings.Contains(output, "/workspace/downloads/") {
		t.Fatalf("tool output did not expose staged path: %q", output)
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

	tool := &imageGenerateTool{db: db, uploadDir: root, artifactDir: root}
	images, tooMany := tool.loadInputImages(ctx, &llm.ToolContext{DB: db, UserID: "u1", ConvID: "c1"}, []string{"same", "cross", "fake"}, 3)
	if tooMany {
		t.Fatal("unexpected reference-image truncation")
	}
	if len(images) != 1 || images[0].mime != "image/png" || string(images[0].data) != string(png) {
		t.Fatalf("verified reference images = %+v, want only same-conversation PNG bytes", images)
	}
}

func TestPythonExecuteRejectsOutOfRootAndSymlinkedDatabasePaths(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	uploads := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.csv")
	if err := os.WriteFile(outside, []byte("secret,value\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(uploads, "linked.csv")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash) VALUES('u_jail','jail@example.test','h')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c_jail','u_jail','Jail')`,
		`INSERT INTO messages(id,conversation_id,role,author_id,status) VALUES('msg_jail','c_jail','assistant','u_jail','streaming')`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []store.File{
		{ID: "proc", UserID: "u_jail", ConversationID: "c_jail", Filename: "proc.csv", MimeType: "text/csv", Kind: "sheet", SizeBytes: 32, StoragePath: "/proc/self/environ"},
		{ID: "link", UserID: "u_jail", ConversationID: "c_jail", Filename: "linked.csv", MimeType: "text/csv", Kind: "sheet", SizeBytes: 16, StoragePath: link},
	} {
		if _, err := store.CreateFile(ctx, db, file); err != nil {
			t.Fatal(err)
		}
	}
	fake := &recordingSandbox{execResult: &sandbox.Result{Stdout: "done\n"}}
	tool := &pythonExecuteTool{sandbox: fake, uploadDir: uploads, artifactDir: t.TempDir(), logger: log.New(io.Discard, "", 0)}
	if _, _, err := tool.Execute(ctx, []byte(`{"code":"print('done')"}`), &llm.ToolContext{
		UserID: "u_jail", ConvID: "c_jail", MessageID: "msg_jail", DB: db, BuiltinTools: map[string]bool{"python_execute": true},
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fake.putFiles) != 0 {
		t.Fatalf("unsafe DB paths reached sandbox: %+v", fake.putFiles)
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
