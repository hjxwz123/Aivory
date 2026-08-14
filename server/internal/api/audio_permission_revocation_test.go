package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

type audioRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn audioRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func TestAudioTranscriptionTracksCurrentVoicePermission(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutate     func(store.UserGroupPermissions) store.UserGroupPermissions
		wantStatus int
		wantCancel bool
	}{
		{
			name: "voice permission revoked",
			mutate: func(permissions store.UserGroupPermissions) store.UserGroupPermissions {
				permissions.AllowVoiceTranscription = false
				return permissions
			},
			wantStatus: http.StatusForbidden,
			wantCancel: true,
		},
		{
			name: "unrelated permission changed",
			mutate: func(permissions store.UserGroupPermissions) store.UserGroupPermissions {
				permissions.AllowDrawing = false
				return permissions
			},
			wantStatus: http.StatusOK,
			wantCancel: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openMigrated(t, filepath.Join(t.TempDir(), "audio-permission.db"))
			defer db.Close()
			allowed := store.DefaultUserGroupPermissions()
			allowedRaw, err := json.Marshal(allowed)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, db, `INSERT INTO user_groups(id,name,permissions) VALUES('voice-group','Voice',?)`, string(allowedRaw))
			mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status,group_id)
				VALUES('voice-user','voice@example.test','h','user','active','voice-group')`)

			upstreamStarted := make(chan struct{})
			upstreamCanceled := make(chan struct{})
			releaseUpstream := make(chan struct{})
			var cancelOnce sync.Once
			var releaseOnce sync.Once
			previousAudioHTTPClient := audioHTTPClient
			audioHTTPClient = &http.Client{Transport: audioRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				close(upstreamStarted)
				select {
				case <-r.Context().Done():
					cancelOnce.Do(func() { close(upstreamCanceled) })
					return nil, r.Context().Err()
				case <-releaseUpstream:
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"text":"allowed transcript"}`)),
						Request:    r,
					}, nil
				}
			})}
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(releaseUpstream) })
				audioHTTPClient = previousAudioHTTPClient
			})
			if err := store.SetSetting(db, "audio_transcribe_base_url", "http://audio-upstream.test"); err != nil {
				t.Fatal(err)
			}
			if err := store.SetSetting(db, "audio_transcribe_api_key", "test-key"); err != nil {
				t.Fatal(err)
			}

			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", "voice.wav")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte("audio payload")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/audio/transcriptions", bytes.NewReader(body.Bytes()))
			req.Header.Set("content-type", writer.FormDataContentType())
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{
				ID: "voice-user", Role: "user", Status: "active", GroupID: "voice-group",
			}))
			recorder := httptest.NewRecorder()
			deps := Deps{DB: db, Cache: cache.NewMemory()}

			done := make(chan struct{})
			go func() {
				transcribeAudioHandler(deps, recorder, req)
				close(done)
			}()
			select {
			case <-upstreamStarted:
			case <-time.After(3 * time.Second):
				t.Fatal("transcription upstream did not start")
			}

			changed := test.mutate(allowed)
			changedRaw, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, db, `UPDATE user_groups SET permissions=? WHERE id='voice-group'`, string(changedRaw))
			revokeGroupPermissionSnapshots(deps, "voice-group")

			if test.wantCancel {
				select {
				case <-upstreamCanceled:
				case <-time.After(3 * time.Second):
					t.Fatal("revoked transcription did not cancel its upstream request")
				}
			} else {
				select {
				case <-upstreamCanceled:
					t.Fatal("unrelated permission change canceled transcription")
				case <-time.After(100 * time.Millisecond):
				}
				releaseOnce.Do(func() { close(releaseUpstream) })
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("transcription handler did not finish")
			}
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), test.wantStatus)
			}
			if test.wantCancel && !strings.Contains(recorder.Body.String(), errVoiceGroupPermission.Error()) {
				t.Fatalf("revoked response body=%s", recorder.Body.String())
			}
			if !test.wantCancel && !strings.Contains(recorder.Body.String(), "allowed transcript") {
				t.Fatalf("allowed response body=%s", recorder.Body.String())
			}
		})
	}
}

func TestCapabilityAccessWatcherRevokesVoicePermission(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "capability-watcher.db"))
	defer db.Close()
	allowed := store.DefaultUserGroupPermissions()
	allowedRaw, err := json.Marshal(allowed)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,permissions) VALUES('voice-group','Voice',?)`, string(allowedRaw))
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status,group_id)
		VALUES('voice-user','voice@example.test','h','user','active','voice-group')`)

	deps := Deps{DB: db, Cache: cache.NewMemory()}
	watcher, err := startCapabilityAccessWatcher(
		deps, context.Background(), "voice-user", errVoiceGroupPermission,
		func(permissions store.UserGroupPermissions) bool { return permissions.AllowVoiceTranscription },
	)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	permissions := allowed
	permissions.AllowVoiceTranscription = false
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE user_groups SET permissions=? WHERE id='voice-group'`, string(raw))
	state, err := store.UserGroupPermissionStateForUser(context.Background(), db, "voice-user")
	if err != nil {
		t.Fatal(err)
	}
	if state.Permissions.AllowVoiceTranscription {
		t.Fatal("database permission remained enabled after update")
	}
	revokeGroupPermissionSnapshots(deps, "voice-group")

	select {
	case <-watcher.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("voice capability watcher did not cancel after permission revocation")
	}
	if !watcher.Revoked() {
		t.Fatal("voice capability watcher canceled without recording revocation")
	}
}
