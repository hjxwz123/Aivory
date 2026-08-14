package api

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"aivory/server/internal/store"
)

const capabilityWatcherFallbackPollInterval = time.Second

type capabilityAccessSnapshot struct {
	permissions    store.UserGroupPermissions
	groupID        string
	groupExpiresAt int64
	userEpoch      string
	groupEpoch     string
}

type capabilityAccessSubscription struct {
	userEvents  <-chan string
	groupEvents <-chan string
	unsubUser   func()
	unsubGroup  func()
	timer       *time.Timer
}

func (subscription *capabilityAccessSubscription) close() {
	if subscription == nil {
		return
	}
	if subscription.timer != nil {
		subscription.timer.Stop()
	}
	if subscription.unsubUser != nil {
		subscription.unsubUser()
	}
	if subscription.unsubGroup != nil {
		subscription.unsubGroup()
	}
}

func (subscription *capabilityAccessSubscription) timerChannel() <-chan time.Time {
	if subscription == nil || subscription.timer == nil {
		return nil
	}
	return subscription.timer.C
}

// capabilityAccessWatcher keeps a long-lived upstream operation aligned with
// the user's current group capability. Permission events trigger an
// authoritative re-read instead of unconditional cancellation, so unrelated
// administrator edits and moves between equally-capable groups do not disrupt
// the operation.
type capabilityAccessWatcher struct {
	d       Deps
	userID  string
	allowed func(store.UserGroupPermissions) bool
	denied  error
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	revoked atomic.Bool
}

func startCapabilityAccessWatcher(
	d Deps,
	parent context.Context,
	userID string,
	denied error,
	allowed func(store.UserGroupPermissions) bool,
) (*capabilityAccessWatcher, error) {
	if strings.TrimSpace(userID) == "" || allowed == nil {
		return nil, denied
	}
	ctx, cancel := context.WithCancel(parent)
	watcher := &capabilityAccessWatcher{
		d: d, userID: userID, allowed: allowed, denied: denied,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	subscription, err := watcher.arm()
	if err != nil {
		cancel()
		return nil, err
	}
	go watcher.run(subscription)
	return watcher, nil
}

func (watcher *capabilityAccessWatcher) Context() context.Context {
	if watcher == nil {
		return context.Background()
	}
	return watcher.ctx
}

func (watcher *capabilityAccessWatcher) Revoked() bool {
	return watcher != nil && watcher.revoked.Load()
}

func (watcher *capabilityAccessWatcher) AllowedNow() bool {
	if watcher == nil || watcher.Revoked() {
		return false
	}
	snapshot, err := currentCapabilityAccessSnapshot(watcher.ctx, watcher.d, watcher.userID)
	return err == nil && watcher.allowed(snapshot.permissions)
}

func (watcher *capabilityAccessWatcher) Close() {
	if watcher == nil {
		return
	}
	watcher.cancel()
	<-watcher.done
}

func (watcher *capabilityAccessWatcher) revoke() {
	watcher.revoked.Store(true)
	watcher.cancel()
}

func (watcher *capabilityAccessWatcher) run(subscription *capabilityAccessSubscription) {
	defer close(watcher.done)
	for {
		closedUnexpectedly := false
		select {
		case _, ok := <-subscription.userEvents:
			closedUnexpectedly = !ok
		case _, ok := <-subscription.groupEvents:
			closedUnexpectedly = !ok
		case <-subscription.timerChannel():
		case <-watcher.ctx.Done():
			subscription.close()
			return
		}
		subscription.close()
		if closedUnexpectedly {
			watcher.revoke()
			return
		}
		next, err := watcher.arm()
		if err != nil {
			watcher.revoke()
			return
		}
		subscription = next
	}
}

func (watcher *capabilityAccessWatcher) arm() (*capabilityAccessSubscription, error) {
	for attempts := 0; attempts < 3; attempts++ {
		before, err := currentCapabilityAccessSnapshot(watcher.ctx, watcher.d, watcher.userID)
		if err != nil {
			return nil, err
		}
		if !watcher.allowed(before.permissions) {
			return nil, watcher.denied
		}

		subscription := &capabilityAccessSubscription{unsubUser: func() {}, unsubGroup: func() {}}
		if watcher.d.Cache != nil {
			var userEvents chan string
			userEvents, subscription.unsubUser = watcher.d.Cache.Subscribe(userPermissionRevocationTopic(watcher.userID))
			subscription.userEvents = userEvents
			if strings.TrimSpace(before.groupID) != "" {
				var groupEvents chan string
				groupEvents, subscription.unsubGroup = watcher.d.Cache.Subscribe(groupPermissionRevocationTopic(before.groupID))
				subscription.groupEvents = groupEvents
			}
		}

		// Subscribe before the second read. A permission mutation on either side
		// of setup is visible through the version comparison or a queued event.
		after, err := currentCapabilityAccessSnapshot(watcher.ctx, watcher.d, watcher.userID)
		if err != nil {
			subscription.close()
			return nil, err
		}
		if !watcher.allowed(after.permissions) {
			subscription.close()
			return nil, watcher.denied
		}
		if !capabilityAccessSnapshotsStable(before, after) {
			subscription.close()
			continue
		}

		pollAfter := time.Duration(0)
		if watcher.d.Cache == nil {
			pollAfter = capabilityWatcherFallbackPollInterval
		}
		if after.groupExpiresAt > 0 {
			untilExpiry := time.Until(time.Unix(after.groupExpiresAt, 0))
			if untilExpiry <= 0 {
				subscription.close()
				continue
			}
			if pollAfter == 0 || untilExpiry < pollAfter {
				pollAfter = untilExpiry
			}
		}
		if pollAfter > 0 {
			subscription.timer = time.NewTimer(pollAfter)
		}
		return subscription, nil
	}
	return nil, errPermissionDenied
}

func currentCapabilityAccessSnapshot(ctx context.Context, d Deps, userID string) (capabilityAccessSnapshot, error) {
	for attempts := 0; attempts < 3; attempts++ {
		before := currentPermissionEpoch(d)
		state, err := store.UserGroupPermissionStateForUser(ctx, d.DB, userID)
		if err != nil {
			return capabilityAccessSnapshot{}, err
		}
		after := currentPermissionEpoch(d)
		if before != after {
			continue
		}
		return capabilityAccessSnapshot{
			permissions:    state.Permissions,
			groupID:        state.GroupID,
			groupExpiresAt: state.GroupExpiresAt,
			userEpoch:      permissionGenerationEpoch(d, "user", userID),
			groupEpoch:     permissionGenerationEpoch(d, "group", state.GroupID),
		}, nil
	}
	return capabilityAccessSnapshot{}, errPermissionDenied
}

func capabilityAccessSnapshotsStable(a, b capabilityAccessSnapshot) bool {
	return a.groupID == b.groupID &&
		a.groupExpiresAt == b.groupExpiresAt &&
		a.userEpoch == b.userEpoch &&
		a.groupEpoch == b.groupEpoch &&
		store.UserGroupPermissionsEqual(a.permissions, b.permissions)
}

func isCapabilityDenied(err, denied error) bool {
	return errors.Is(err, denied) || errors.Is(err, store.ErrNotFound) || errors.Is(err, errPermissionDenied)
}
