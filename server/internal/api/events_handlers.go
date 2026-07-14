package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/envcfg"
)

// §23 realtime notify stream — one long-lived SSE connection per open tab
// (`GET /api/events`) over which the server pushes thin "something changed"
// events, so another device/tab of the same user updates without polling:
//
//	{"type":"conversation.updated","conversation_id":"c_1","origin":"d-x"}
//	{"type":"conversation.created", ...} / {"type":"conversation.deleted", ...}
//	{"type":"hello"}   — sent once on connect; the client uses it to (re)check
//	                     the app version and run reconnect compensation.
//
// Cross-instance fan-out rides the existing cache Pub/Sub bus (Redis when
// configured, in-process otherwise — same bus as cfg:invalidate): every event
// is PUBLISHed as an envelope on ONE shared topic; each instance holds ONE
// subscription and routes to its locally-connected tabs. This keeps the Redis
// cost at one subscription per instance (never per connection) and adds zero
// database load — events carry ids only, receivers re-fetch through the
// existing authorized read endpoints.
//
// Delivery is intentionally best-effort: sends never block (drop on a full
// per-connection buffer), because the client reconciles by re-fetching. A
// dropped event costs one missed live refresh, never data.

var (
	// eventsHeartbeatInterval keeps intermediaries (nginx, LBs) from idling the
	// connection out; SSE comments are invisible to EventSource-style parsers.
	eventsHeartbeatInterval = envcfg.Dur("AIVORY_API_EVENTS_HEARTBEAT", 25*time.Second)
	// eventsMaxConnsPerUser caps runaway tab counts; opening one more evicts the
	// oldest stream (that tab will silently reconnect and evict the next-oldest,
	// which is harmless churn only in the pathological >cap case).
	eventsMaxConnsPerUser = envcfg.Int("AIVORY_API_EVENTS_MAX_CONNS_PER_USER", 16)
	// eventsConnBuffer is the per-connection queue; overflow drops the event
	// (client-side compensation re-fetches on the next event / reconnect).
	eventsConnBuffer = envcfg.Int("AIVORY_API_EVENTS_CONN_BUFFER", 16)
)

// eventsTopic is the single cross-instance Pub/Sub topic. Envelopes carry the
// target user id so each instance can route locally without per-user topics
// (a Redis subscription per user/connection would not scale).
const eventsTopic = "user-events"

type userEventEnvelope struct {
	UserID  string `json:"u"`
	Payload string `json:"p"`
}

// eventsConn is one open /api/events stream.
type eventsConn struct {
	ch   chan string
	done chan struct{} // closed on eviction (per-user cap)
}

type eventsHubT struct {
	mu    sync.Mutex
	conns map[string][]*eventsConn
	once  sync.Once
}

// eventsHub is the instance-local connection registry. Package-level singleton
// so no Deps/main.go wiring is needed; the bus subscription starts lazily on
// first use with the process-wide cache handed in by any handler.
var eventsHub = &eventsHubT{conns: map[string][]*eventsConn{}}

// ensureStarted opens this instance's single bus subscription (idempotent).
func (h *eventsHubT) ensureStarted(c cache.Cache) {
	if c == nil {
		return
	}
	h.once.Do(func() {
		ch, _ := c.Subscribe(eventsTopic)
		go func() {
			for raw := range ch {
				var env userEventEnvelope
				if err := json.Unmarshal([]byte(raw), &env); err == nil && env.UserID != "" && env.Payload != "" {
					h.deliver(env.UserID, env.Payload)
				}
			}
		}()
	})
}

func (h *eventsHubT) register(userID string) *eventsConn {
	conn := &eventsConn{ch: make(chan string, eventsConnBuffer), done: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.conns[userID]
	// Per-user cap: evict the oldest stream rather than rejecting the newest tab.
	for len(list) >= eventsMaxConnsPerUser && len(list) > 0 {
		close(list[0].done)
		list = list[1:]
	}
	h.conns[userID] = append(list, conn)
	return conn
}

func (h *eventsHubT) unregister(userID string, conn *eventsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.conns[userID]
	for i, c := range list {
		if c == conn {
			h.conns[userID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(h.conns[userID]) == 0 {
		delete(h.conns, userID)
	}
	// conn.ch is never closed — deliver() may hold a stale reference for a
	// moment and a send to a closed channel panics; an abandoned buffered
	// channel is just garbage-collected instead.
}

// deliver fans a payload out to the user's local connections, never blocking:
// a full buffer drops the event (the client re-fetches on the next one).
func (h *eventsHubT) deliver(userID, payload string) {
	h.mu.Lock()
	conns := make([]*eventsConn, len(h.conns[userID]))
	copy(conns, h.conns[userID])
	h.mu.Unlock()
	for _, c := range conns {
		select {
		case c.ch <- payload:
		default:
		}
	}
}

// publishUserEvent fans a realtime event out to every open /api/events stream
// of this user on every instance. `origin` (the sender tab's X-Device-Id) lets
// receivers suppress their own echo. Fire-and-forget: never fails the caller.
func publishUserEvent(d Deps, r *http.Request, userID, eventType, conversationID string) {
	if d.Cache == nil || userID == "" {
		return
	}
	eventsHub.ensureStarted(d.Cache)
	ev := map[string]string{"type": eventType}
	if conversationID != "" {
		ev["conversation_id"] = conversationID
	}
	if r != nil {
		if origin := strings.TrimSpace(r.Header.Get("X-Device-Id")); origin != "" && len(origin) <= 64 {
			ev["origin"] = origin
		}
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return
	}
	env, err := json.Marshal(userEventEnvelope{UserID: userID, Payload: string(payload)})
	if err != nil {
		return
	}
	d.Cache.Publish(eventsTopic, string(env))
}

// eventsStreamHandler is the long-lived per-tab notification stream. It only
// ever WRITES server events; nothing here reads request data beyond auth, and
// it holds no DB or model resources — just a channel registration + heartbeat.
func eventsStreamHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, fmt.Errorf("streaming unsupported"))
		return
	}
	eventsHub.ensureStarted(d.Cache)
	// The server-wide WriteTimeout (sized for generation SSE) is an absolute
	// per-response deadline — heartbeats do not extend it, so it would sever
	// every notify stream at that mark. Clear it for this endpoint only; the
	// heartbeat + client reconnect handle genuinely dead peers.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable proxy buffering (nginx) so events flush immediately.
	w.Header().Set("X-Accel-Buffering", "no")

	conn := eventsHub.register(u.ID)
	defer eventsHub.unregister(u.ID, conn)

	// Realtime ban (§8.1): banUserAdmin publishes user:{id}:kill and every
	// long-lived per-user stream honors it — including this one.
	var killCh <-chan string
	if d.Cache != nil {
		ch, unsubKill := d.Cache.Subscribe("user:" + u.ID + ":kill")
		defer unsubKill()
		killCh = ch
	}

	// Hello: confirms the stream to the client, which uses it to trigger a
	// version check + reconnect compensation.
	fmt.Fprint(w, "data: {\"type\":\"hello\"}\n\n")
	fl.Flush()

	heartbeat := time.NewTicker(eventsHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-conn.done: // evicted by the per-user cap
			return
		case <-killCh: // realtime ban (§8.1)
			return
		case p := <-conn.ch:
			fmt.Fprintf(w, "data: %s\n\n", p)
			fl.Flush()
		case <-heartbeat.C:
			// SSE comment line — keeps proxies from idling the connection out.
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
