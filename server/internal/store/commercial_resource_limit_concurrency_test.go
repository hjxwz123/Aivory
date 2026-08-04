package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

type commercialLimitFixture struct {
	db         *sql.DB
	ctx        context.Context
	workspaceA string
	workspaceB string
}

func newCommercialLimitFixture(t *testing.T) commercialLimitFixture {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "commercial-resource-limit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	for _, id := range []string{"owner-a", "owner-b", "creator"} {
		exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?,'h','user','active')`, id, id+"@example.test")
	}
	exec(t, db, `INSERT INTO channels(id,name,type) VALUES('commercial-cap-channel','Embedding','openai')`)
	exec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES('commercial-cap-embed','commercial-cap-channel','embedding','embed','Embedding',3)`)

	workspaceA, err := CreateWorkspace(ctx, db, "owner-a", "Commercial A")
	if err != nil {
		t.Fatalf("create workspace A: %v", err)
	}
	workspaceB, err := CreateWorkspace(ctx, db, "owner-b", "Commercial B")
	if err != nil {
		t.Fatalf("create workspace B: %v", err)
	}
	for _, workspaceID := range []string{workspaceA.ID, workspaceB.ID} {
		if err := JoinWorkspace(ctx, db, workspaceID, "creator"); err != nil {
			t.Fatalf("join workspace %s: %v", workspaceID, err)
		}
	}
	return commercialLimitFixture{
		db: db, ctx: ctx, workspaceA: workspaceA.ID, workspaceB: workspaceB.ID,
	}
}

func runCommercialLimitRace(t *testing.T, creates ...func() error) []error {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, len(creates))
	var wg sync.WaitGroup
	for _, create := range creates {
		create := create
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- create()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	out := make([]error, 0, len(creates))
	for err := range errs {
		out = append(out, err)
	}
	return out
}

func assertCommercialLimitRace(t *testing.T, errs []error, limitErr error) {
	t.Helper()
	succeeded, rejected := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, limitErr):
			rejected++
		default:
			t.Fatalf("unexpected concurrent create error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent creates succeeded=%d limit_rejected=%d, want 1/1", succeeded, rejected)
	}
}

func TestConcurrentProjectCreatesCannotExceedCommercialLimit(t *testing.T) {
	t.Run("personal and workspace", func(t *testing.T) {
		fx := newCommercialLimitFixture(t)
		errs := runCommercialLimitRace(t,
			func() error {
				_, err := CreateProjectWithLimit(fx.ctx, fx.db, Project{
					ID: "personal-project", UserID: "creator", Name: "Personal project",
				}, 1)
				return err
			},
			func() error {
				_, err := CreateProjectWithLimit(fx.ctx, fx.db, Project{
					ID: "workspace-project", UserID: "creator", WorkspaceID: fx.workspaceA, Name: "Workspace project",
				}, 1)
				return err
			},
		)
		assertCommercialLimitRace(t, errs, ErrProjectLimitExceeded)
		assertProjectCommercialLimitCount(t, fx)
	})

	t.Run("different workspaces", func(t *testing.T) {
		fx := newCommercialLimitFixture(t)
		errs := runCommercialLimitRace(t,
			func() error {
				_, err := CreateProjectWithLimit(fx.ctx, fx.db, Project{
					ID: "workspace-a-project", UserID: "creator", WorkspaceID: fx.workspaceA, Name: "Workspace A project",
				}, 1)
				return err
			},
			func() error {
				_, err := CreateProjectWithLimit(fx.ctx, fx.db, Project{
					ID: "workspace-b-project", UserID: "creator", WorkspaceID: fx.workspaceB, Name: "Workspace B project",
				}, 1)
				return err
			},
		)
		assertCommercialLimitRace(t, errs, ErrProjectLimitExceeded)
		assertProjectCommercialLimitCount(t, fx)
	})
}

func assertProjectCommercialLimitCount(t *testing.T, fx commercialLimitFixture) {
	t.Helper()
	if n, err := CountProjectsByUser(fx.ctx, fx.db, "creator"); err != nil || n != 1 {
		t.Fatalf("commercial project count=%d err=%v, want 1", n, err)
	}
	var rows int
	if err := fx.db.QueryRowContext(fx.ctx, `SELECT COUNT(*) FROM projects WHERE user_id='creator'`).Scan(&rows); err != nil {
		t.Fatalf("count creator project rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("creator project rows=%d, want 1", rows)
	}
}

func TestConcurrentStandaloneKBCreateCannotExceedCommercialLimit(t *testing.T) {
	create := func(fx commercialLimitFixture, id, name, workspaceID string) func() error {
		return func() error {
			_, err := CreateKBWithLimit(fx.ctx, fx.db, KnowledgeBase{
				ID: id, UserID: "creator", Name: name, WorkspaceID: workspaceID,
				EmbeddingModelID: "commercial-cap-embed", EmbeddingDim: 3,
			}, 1)
			return err
		}
	}

	t.Run("personal and workspace", func(t *testing.T) {
		fx := newCommercialLimitFixture(t)
		errs := runCommercialLimitRace(t,
			create(fx, "personal-kb", "Personal KB", ""),
			create(fx, "workspace-kb", "Workspace KB", fx.workspaceA),
		)
		assertCommercialLimitRace(t, errs, ErrKBLimitExceeded)
		assertKBCommercialLimitCount(t, fx)
	})

	t.Run("different workspaces", func(t *testing.T) {
		fx := newCommercialLimitFixture(t)
		errs := runCommercialLimitRace(t,
			create(fx, "workspace-a-kb", "Workspace A KB", fx.workspaceA),
			create(fx, "workspace-b-kb", "Workspace B KB", fx.workspaceB),
		)
		assertCommercialLimitRace(t, errs, ErrKBLimitExceeded)
		assertKBCommercialLimitCount(t, fx)
	})
}

func assertKBCommercialLimitCount(t *testing.T, fx commercialLimitFixture) {
	t.Helper()
	if n, err := CountStandaloneKBsByUser(fx.ctx, fx.db, "creator"); err != nil || n != 1 {
		t.Fatalf("commercial KB count=%d err=%v, want 1", n, err)
	}
	var rows int
	if err := fx.db.QueryRowContext(fx.ctx, `SELECT COUNT(*) FROM knowledge_bases WHERE user_id='creator'`).Scan(&rows); err != nil {
		t.Fatalf("count creator KB rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("creator KB rows=%d, want 1", rows)
	}
}

func TestProjectLibraryDoesNotConsumeStandaloneKBLimit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		workspaceID func(commercialLimitFixture) string
	}{
		{name: "personal", workspaceID: func(commercialLimitFixture) string { return "" }},
		{name: "workspace", workspaceID: func(fx commercialLimitFixture) string { return fx.workspaceA }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCommercialLimitFixture(t)
			workspaceID := tc.workspaceID(fx)
			library, err := CreateKB(fx.ctx, fx.db, KnowledgeBase{
				ID: "project-library", UserID: "creator", Name: "Project library", WorkspaceID: workspaceID,
				EmbeddingModelID: "commercial-cap-embed", EmbeddingDim: 3,
			})
			if err != nil {
				t.Fatalf("create project library: %v", err)
			}
			if _, err := CreateProject(fx.ctx, fx.db, Project{
				ID: "library-project", UserID: "creator", Name: "Library project",
				KBID: library.ID, WorkspaceID: workspaceID,
			}); err != nil {
				t.Fatalf("create project: %v", err)
			}
			if n, err := CountStandaloneKBsByUser(fx.ctx, fx.db, "creator"); err != nil || n != 0 {
				t.Fatalf("standalone count with project library=%d err=%v, want 0", n, err)
			}
			if _, err := CreateKBWithLimit(fx.ctx, fx.db, KnowledgeBase{
				ID: "standalone-kb", UserID: "creator", Name: "Standalone KB", WorkspaceID: workspaceID,
				EmbeddingModelID: "commercial-cap-embed", EmbeddingDim: 3,
			}, 1); err != nil {
				t.Fatalf("create first standalone KB under limit: %v", err)
			}
			if n, err := CountStandaloneKBsByUser(fx.ctx, fx.db, "creator"); err != nil || n != 1 {
				t.Fatalf("standalone count after create=%d err=%v, want 1", n, err)
			}
		})
	}
}

func TestAtomicWorkspaceProjectLibraryCreateSerializesConcurrentKick(t *testing.T) {
	fx := newCommercialLimitFixture(t)
	libraryInserted := make(chan struct{})
	allowProjectInsert := make(chan struct{})
	createDone := make(chan error, 1)
	go func() {
		_, err := createProjectWithLimit(fx.ctx, fx.db, Project{
			ID: "atomic-workspace-project", UserID: "creator", Name: "Atomic workspace project",
			WorkspaceID: fx.workspaceA,
		}, &KnowledgeBase{
			ID: "atomic-workspace-library", UserID: "creator", Name: "Atomic workspace library",
			EmbeddingModelID: "commercial-cap-embed", EmbeddingDim: 3, WorkspaceID: fx.workspaceA,
		}, 1, func() error {
			close(libraryInserted)
			<-allowProjectInsert
			return nil
		})
		createDone <- err
	}()
	<-libraryInserted

	kickAttempting := make(chan struct{})
	kickDone := make(chan error, 1)
	go func() {
		close(kickAttempting)
		kickDone <- RemoveWorkspaceMember(fx.ctx, fx.db, fx.workspaceA, "creator")
	}()
	<-kickAttempting
	close(allowProjectInsert)

	if err := <-createDone; err != nil {
		t.Fatalf("atomic project create: %v", err)
	}
	if err := <-kickDone; err != nil {
		t.Fatalf("concurrent kick: %v", err)
	}
	var kbID string
	if err := fx.db.QueryRowContext(fx.ctx,
		`SELECT COALESCE(kb_id,'') FROM projects WHERE id='atomic-workspace-project'`,
	).Scan(&kbID); err != nil {
		t.Fatalf("read committed project: %v", err)
	}
	if kbID != "atomic-workspace-library" {
		t.Fatalf("project kb_id=%q, want atomic-workspace-library", kbID)
	}
	assertNoUnattachedProjectLibrary(t, fx, "atomic-workspace-library")
	var membership int
	if err := fx.db.QueryRowContext(fx.ctx,
		`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id='creator'`, fx.workspaceA,
	).Scan(&membership); err != nil {
		t.Fatalf("count membership: %v", err)
	}
	if membership != 0 {
		t.Fatalf("membership rows=%d, want kicked creator absent", membership)
	}
}

func TestAtomicProjectLibraryFailuresLeaveNoOrphan(t *testing.T) {
	t.Run("failure after library insert", func(t *testing.T) {
		fx := newCommercialLimitFixture(t)
		injected := errors.New("injected post-library failure")
		_, err := createProjectWithLimit(fx.ctx, fx.db, Project{
			ID: "failed-project", UserID: "creator", Name: "Failed project", WorkspaceID: fx.workspaceA,
		}, &KnowledgeBase{
			ID: "failed-library", UserID: "creator", Name: "Failed library",
			EmbeddingModelID: "commercial-cap-embed", EmbeddingDim: 3, WorkspaceID: fx.workspaceA,
		}, 1, func() error { return injected })
		if !errors.Is(err, injected) {
			t.Fatalf("create error=%v, want injected failure", err)
		}
		assertProjectAndLibraryAbsent(t, fx, "failed-project", "failed-library")
	})

	t.Run("project name conflict after library insert", func(t *testing.T) {
		fx := newCommercialLimitFixture(t)
		if _, err := CreateProject(fx.ctx, fx.db, Project{
			ID: "existing-project", UserID: "creator", Name: "Conflicting project", WorkspaceID: fx.workspaceA,
		}); err != nil {
			t.Fatalf("seed conflicting project: %v", err)
		}
		_, err := CreateProjectWithLibraryAndLimit(fx.ctx, fx.db, Project{
			ID: "conflicting-project", UserID: "creator", Name: "Conflicting project", WorkspaceID: fx.workspaceA,
		}, KnowledgeBase{
			ID: "conflicting-library", UserID: "creator", Name: "Conflict rollback library",
			EmbeddingModelID: "commercial-cap-embed", EmbeddingDim: 3, WorkspaceID: fx.workspaceA,
		}, 0)
		if !errors.Is(err, ErrProjectNameExists) {
			t.Fatalf("create error=%v, want ErrProjectNameExists", err)
		}
		assertProjectAndLibraryAbsent(t, fx, "conflicting-project", "conflicting-library")
	})
}

func assertProjectAndLibraryAbsent(t *testing.T, fx commercialLimitFixture, projectID, libraryID string) {
	t.Helper()
	var projects, libraries int
	if err := fx.db.QueryRowContext(fx.ctx, `SELECT COUNT(*) FROM projects WHERE id=?`, projectID).Scan(&projects); err != nil {
		t.Fatalf("count project %s: %v", projectID, err)
	}
	if err := fx.db.QueryRowContext(fx.ctx, `SELECT COUNT(*) FROM knowledge_bases WHERE id=?`, libraryID).Scan(&libraries); err != nil {
		t.Fatalf("count library %s: %v", libraryID, err)
	}
	if projects != 0 || libraries != 0 {
		t.Fatalf("rolled-back project rows=%d library rows=%d, want 0/0", projects, libraries)
	}
}

func assertNoUnattachedProjectLibrary(t *testing.T, fx commercialLimitFixture, libraryID string) {
	t.Helper()
	var libraries, unattached int
	if err := fx.db.QueryRowContext(fx.ctx,
		`SELECT COUNT(*) FROM knowledge_bases WHERE id=?`, libraryID,
	).Scan(&libraries); err != nil {
		t.Fatalf("count library %s: %v", libraryID, err)
	}
	if err := fx.db.QueryRowContext(fx.ctx, `
		SELECT COUNT(*)
		  FROM knowledge_bases k
		  LEFT JOIN projects p ON p.kb_id=k.id
		 WHERE k.id=? AND p.id IS NULL`, libraryID,
	).Scan(&unattached); err != nil {
		t.Fatalf("count unattached library %s: %v", libraryID, err)
	}
	if libraries != 1 || unattached != 0 {
		t.Fatalf("library rows=%d unattached=%d, want 1/0", libraries, unattached)
	}
}
