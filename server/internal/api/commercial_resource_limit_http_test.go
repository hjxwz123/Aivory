package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aivory/server/internal/store"
)

type commercialCreateHTTPResult struct {
	status int
	body   string
}

func commercialCreateRequest(t *testing.T, target, body string, user *store.User) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	return req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
}

func runCommercialCreateHTTPRace(t *testing.T, requests ...func() commercialCreateHTTPResult) []commercialCreateHTTPResult {
	t.Helper()
	start := make(chan struct{})
	results := make(chan commercialCreateHTTPResult, len(requests))
	var wg sync.WaitGroup
	for _, request := range requests {
		request := request
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- request()
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	out := make([]commercialCreateHTTPResult, 0, len(requests))
	for result := range results {
		out = append(out, result)
	}
	return out
}

func assertCommercialCreateHTTPRace(t *testing.T, results []commercialCreateHTTPResult, errorCode string) {
	t.Helper()
	created, forbidden := 0, 0
	for _, result := range results {
		switch result.status {
		case http.StatusCreated:
			created++
		case http.StatusForbidden:
			forbidden++
			var payload map[string]string
			if err := json.Unmarshal([]byte(result.body), &payload); err != nil {
				t.Fatalf("decode forbidden response %q: %v", result.body, err)
			}
			if payload["error"] != errorCode {
				t.Fatalf("forbidden error=%q, want %q", payload["error"], errorCode)
			}
		default:
			t.Fatalf("unexpected concurrent create status=%d body=%s", result.status, result.body)
		}
	}
	if created != 1 || forbidden != 1 {
		t.Fatalf("concurrent HTTP creates created=%d forbidden=%d, want 1/1", created, forbidden)
	}
}

func TestConcurrentProjectHTTPCreateMapsAtomicCommercialLimit(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "project-commercial-limit-http.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO user_groups(id,name,max_projects) VALUES('project-capped','Project capped',1)`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES('project-user','project-user@example.test','h','user','active','project-capped')`)

	d := Deps{DB: db}
	user := &store.User{ID: "project-user", Role: "user", Status: "active", GroupID: "project-capped"}
	create := func(name string) func() commercialCreateHTTPResult {
		return func() commercialCreateHTTPResult {
			rec := httptest.NewRecorder()
			createProjectHandler(d, rec, commercialCreateRequest(t, "/api/projects", `{"name":"`+name+`"}`, user))
			return commercialCreateHTTPResult{status: rec.Code, body: rec.Body.String()}
		}
	}
	results := runCommercialCreateHTTPRace(t, create("First project"), create("Second project"))
	assertCommercialCreateHTTPRace(t, results, errProjectLimit.Error())

	if n, err := store.CountProjectsByUser(context.Background(), db, user.ID); err != nil || n != 1 {
		t.Fatalf("project count=%d err=%v, want 1", n, err)
	}
}

func TestConcurrentProjectWithLibraryHTTPCreateLeavesNoOrphan(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "project-library-commercial-limit-http.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO user_groups(id,name,max_projects) VALUES('project-library-capped','Project library capped',1)`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES('project-library-user','project-library-user@example.test','h','user','active','project-library-capped')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('project-library-channel','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('project-library-embed','project-library-channel','embedding','embed','Embedding',1,3)`)
	if err := store.SetSetting(db, "embedding_model_id", "project-library-embed"); err != nil {
		t.Fatalf("configure project library index: %v", err)
	}

	d := Deps{DB: db}
	user := &store.User{ID: "project-library-user", Role: "user", Status: "active", GroupID: "project-library-capped"}
	create := func(name string) func() commercialCreateHTTPResult {
		return func() commercialCreateHTTPResult {
			rec := httptest.NewRecorder()
			createProjectHandler(d, rec, commercialCreateRequest(t, "/api/projects", `{"name":"`+name+`"}`, user))
			return commercialCreateHTTPResult{status: rec.Code, body: rec.Body.String()}
		}
	}
	results := runCommercialCreateHTTPRace(t, create("First library project"), create("Second library project"))
	assertCommercialCreateHTTPRace(t, results, errProjectLimit.Error())

	var projects, libraries, unattached int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM projects WHERE user_id=?`, user.ID,
	).Scan(&projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM knowledge_bases WHERE user_id=?`, user.ID,
	).Scan(&libraries); err != nil {
		t.Fatalf("count project libraries: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `
		SELECT COUNT(*)
		  FROM knowledge_bases k
		  LEFT JOIN projects p ON p.kb_id=k.id
		 WHERE k.user_id=? AND p.id IS NULL`, user.ID,
	).Scan(&unattached); err != nil {
		t.Fatalf("count unattached project libraries: %v", err)
	}
	if projects != 1 || libraries != 1 || unattached != 0 {
		t.Fatalf("projects=%d libraries=%d unattached=%d, want 1/1/0", projects, libraries, unattached)
	}
	if n, err := store.CountStandaloneKBsByUser(context.Background(), db, user.ID); err != nil || n != 0 {
		t.Fatalf("standalone KB count=%d err=%v, want 0", n, err)
	}
}

func TestConcurrentKBHTTPCreateMapsAtomicCommercialLimit(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "kb-commercial-limit-http.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO user_groups(id,name,max_kbs) VALUES('kb-capped','KB capped',1)`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES('kb-user','kb-user@example.test','h','user','active','kb-capped')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('kb-cap-channel','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('kb-cap-embed','kb-cap-channel','embedding','embed','Embedding',1,3)`)
	if err := store.SetSetting(db, "embedding_model_id", "kb-cap-embed"); err != nil {
		t.Fatalf("configure knowledge-base index: %v", err)
	}

	d := Deps{DB: db}
	user := &store.User{ID: "kb-user", Role: "user", Status: "active", GroupID: "kb-capped"}
	create := func(name string) func() commercialCreateHTTPResult {
		return func() commercialCreateHTTPResult {
			rec := httptest.NewRecorder()
			body := `{"name":"` + name + `"}`
			createKBHandler(d, rec, commercialCreateRequest(t, "/api/kbs", body, user))
			return commercialCreateHTTPResult{status: rec.Code, body: rec.Body.String()}
		}
	}
	results := runCommercialCreateHTTPRace(t, create("First KB"), create("Second KB"))
	assertCommercialCreateHTTPRace(t, results, errKBLimit.Error())

	if n, err := store.CountStandaloneKBsByUser(context.Background(), db, user.ID); err != nil || n != 1 {
		t.Fatalf("KB count=%d err=%v, want 1", n, err)
	}
}
