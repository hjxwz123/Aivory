package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"aivory/server/internal/store"

	"github.com/gorilla/websocket"
)

// A bounded FIFO keeps the latest audio during an upstream renewal or a slow
// write. Forwarded frames are released immediately; no full recording is kept.
type liveAudioQueue struct {
	mu     sync.Mutex
	frames [][]byte
	bytes  int
	limit  int
	ended  bool
	wake   chan struct{}
}

func newLiveAudioQueue() *liveAudioQueue {
	seconds := max(1, min(audioStreamBufferSeconds, 120))
	return &liveAudioQueue{limit: seconds * audioPCMBytesPerSecond, wake: make(chan struct{}, 1)}
}

func (q *liveAudioQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *liveAudioQueue) push(frame []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ended {
		return
	}
	if len(frame) > q.limit {
		frame = frame[len(frame)-q.limit:]
	}
	for len(q.frames) > 0 && q.bytes+len(frame) > q.limit {
		q.bytes -= len(q.frames[0])
		q.frames[0] = nil
		q.frames = q.frames[1:]
	}
	q.frames = append(q.frames, frame)
	q.bytes += len(frame)
	q.signal()
}

func (q *liveAudioQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.frames) == 0 {
		return nil, q.ended
	}
	frame := q.frames[0]
	q.frames[0] = nil
	q.frames = q.frames[1:]
	q.bytes -= len(frame)
	if len(q.frames) > 0 || q.ended {
		q.signal()
	}
	return frame, false
}

func (q *liveAudioQueue) end() {
	q.mu.Lock()
	q.ended = true
	q.mu.Unlock()
	q.signal()
}

func (q *liveAudioQueue) finished() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.ended && len(q.frames) == 0
}

type liveAudioSegmentResult struct {
	text string
	code string
	more bool
	err  error
}

type liveAudioResponse struct {
	response *volcResponse
	err      error
}

func relaySegmentedAudio(d Deps, r *http.Request, browser *websocket.Conn, cfg volcanoConfig) {
	ctx, cancel := context.WithCancel(r.Context())
	if audioStreamMaxDur > 0 {
		cancel()
		ctx, cancel = context.WithTimeout(r.Context(), audioStreamMaxDur)
	}
	defer cancel()
	queue := newLiveAudioQueue()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			kind, data, err := browser.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			if kind == websocket.TextMessage {
				if isEndControl(data) {
					queue.end()
					return
				}
				continue
			}
			if kind == websocket.BinaryMessage && len(data) > 0 {
				if len(data)%2 != 0 {
					cancel()
					return
				}
				queue.push(data)
			}
		}
	}()
	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		<-ctx.Done()
		_ = browser.Close()
	}()
	defer func() {
		cancel()
		_ = browser.Close()
		<-readerDone
		<-closeDone
	}()

	user := authUser(r)
	billing := audioBillingFor(d, user)
	var pending []byte
	for segment := 0; ctx.Err() == nil; segment++ {
		if segment > 0 && len(pending) == 0 && queue.finished() {
			writeStreamEvent(browser, streamEvent{Type: "final"})
			return
		}
		result := relayAudioSegment(ctx, d, user.ID, browser, cfg, queue, &pending, billing, segment == 0)
		if result.err != nil {
			if ctx.Err() == nil {
				if segment > 0 && result.code == "insufficient_credits" {
					writeStreamEvent(browser, streamEvent{Type: "final", Code: "credits_exhausted"})
				} else {
					writeStreamEvent(browser, streamEvent{Type: "error", Code: result.code, Message: result.err.Error()})
				}
			}
			return
		}
		if !result.more {
			writeStreamEvent(browser, streamEvent{Type: "final", Text: result.text, Code: result.code})
			return
		}
		// The renderer commits this text and appends the next segment's partials.
		// The API retains only the current segment, not the entire transcript.
		if err := writeStreamEvent(browser, streamEvent{Type: "segment", Text: result.text}); err != nil {
			return
		}
	}
}

func relayAudioSegment(ctx context.Context, d Deps, userID string, browser *websocket.Conn, cfg volcanoConfig, queue *liveAudioQueue, pending *[]byte, billing *audioBilling, first bool) (result liveAudioSegmentResult) {
	// A short segment also keeps reservation expiry and upstream transcripts
	// bounded, even when total recording time is unlimited.
	duration := max(time.Second, min(audioStreamSegmentDur, 10*time.Minute))
	limit := min(audioStreamByteLimit(), int64(duration/time.Second)*audioPCMBytesPerSecond)
	limit -= limit % 2
	creditCapped := false
	sourceID := store.GenID("asr")
	if billing != nil {
		affordable, err := billing.affordableSeconds(ctx, d)
		if err != nil {
			result.err = errors.New("couldn't check your credit balance")
			return
		}
		if affordable < 1 {
			result.err = errAudioInsufficientCredits
			result.code = "insufficient_credits"
			return
		}
		if affordable < billableSeconds(float64(limit)/audioPCMBytesPerSecond) {
			limit = affordable * audioPCMBytesPerSecond
			creditCapped = true
		}
		seconds := billableSeconds(float64(limit) / audioPCMBytesPerSecond)
		if err := billing.reserve(ctx, d, sourceID, seconds); err != nil {
			result.err = errors.New("couldn't reserve credits")
			if errors.Is(err, errAudioInsufficientCredits) {
				result.err = errAudioInsufficientCredits
				result.code = "insufficient_credits"
			}
			return
		}
	}
	var sent int64
	defer func() {
		seconds := billableSeconds(float64(sent) / audioPCMBytesPerSecond)
		credits := 0.0
		if billing != nil {
			debit, err := billing.settle(ctx, d, sourceID, seconds)
			if err != nil {
				result.err = errors.New("voice transcription billing failed")
				result.code = "billing_failed"
				if d.Logger != nil {
					d.Logger.Printf("live voice charge failed (source=%s): %v", sourceID, err)
				}
				return
			}
			credits = debit.Total
		}
		if seconds > 0 {
			recordAudioUsage(ctx, d, userID, sourceID, seconds, billing, credits)
		}
	}()

	upstream, err := dialVolcano(ctx, cfg)
	if err != nil {
		result.err = errors.New("couldn't reach the transcription service")
		if d.Logger != nil {
			d.Logger.Printf("live voice segment dial failed: %v", err)
		}
		return
	}
	segmentCtx, cancel := context.WithCancel(ctx)
	responses := make(chan liveAudioResponse, 4)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			response, err := upstream.readResponse()
			select {
			case responses <- liveAudioResponse{response, err}:
			case <-segmentCtx.Done():
				return
			}
			if err != nil || response.IsLastPackage || response.Code != 0 {
				return
			}
		}
	}()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		<-segmentCtx.Done()
		upstream.close()
	}()
	defer func() { cancel(); upstream.close(); <-readDone; <-closed }()
	if first {
		if err := writeStreamEvent(browser, streamEvent{Type: "ready"}); err != nil {
			result.err = err
			return
		}
	}
	rollover := time.NewTimer(duration)
	defer rollover.Stop()
	var finishing <-chan time.Time
	var finishTimer *time.Timer
	defer func() {
		if finishTimer != nil {
			finishTimer.Stop()
		}
	}()
	finish := func(more bool, code string) bool {
		if finishing != nil {
			return true
		}
		result.more, result.code = more, code
		if err := upstream.sendLast(nil); err != nil {
			result.err = errors.New("the transcription stream ended unexpectedly")
			return false
		}
		finishTimer = time.NewTimer(8 * time.Second)
		finishing = finishTimer.C
		return true
	}

	for {
		if finishing == nil {
			frame := *pending
			*pending = nil
			ended := false
			if len(frame) == 0 {
				frame, ended = queue.pop()
			}
			if ended {
				if !finish(false, "") {
					return
				}
			} else if len(frame) > 0 {
				remaining := limit - sent
				if int64(len(frame)) > remaining {
					*pending = frame[remaining:]
					frame = frame[:remaining]
				}
				if err := upstream.sendAudio(frame); err != nil {
					result.err = errors.New("the transcription stream ended unexpectedly")
					return
				}
				sent += int64(len(frame))
				if sent >= limit {
					more, code := true, ""
					if creditCapped {
						more, code = false, "credits_exhausted"
					}
					if !finish(more, code) {
						return
					}
				}
				// Prefer pending responses between frames so a buffered upload
				// cannot starve partial transcripts or the segment deadline.
				queue.signal()
			}
		}
		select {
		case <-ctx.Done():
			result.err = ctx.Err()
			return
		case <-rollover.C:
			if !finish(true, "") {
				return
			}
		case <-finishing:
			result.err = errors.New("the transcription service did not finish the audio segment")
			return
		case event := <-responses:
			if event.err != nil {
				result.err = errors.New("the transcription stream ended unexpectedly")
				return
			}
			response := event.response
			if asrDebug && d.Logger != nil {
				raw := response.Raw
				if len(raw) > 400 {
					raw = raw[:400]
				}
				d.Logger.Printf("volcano segment frame: code=%d last=%v textlen=%d raw=%s", response.Code, response.IsLastPackage, len(response.Text), raw)
			}
			if response.Code != 0 {
				if d.Logger != nil {
					d.Logger.Printf("volcano ASR error (code=%d logid=%s): %s", response.Code, upstream.logID, response.ErrMessage)
				}
				result.err = errors.New("the transcription service reported an error")
				return
			}
			if response.Text != "" {
				result.text = response.Text
				if err := writeStreamEvent(browser, streamEvent{Type: "partial", Text: result.text}); err != nil {
					result.err = err
					return
				}
			}
			if response.IsLastPackage {
				if sent == 0 && finishing == nil {
					result.err = errors.New("the transcription service ended before receiving audio")
					return
				}
				if finishing == nil {
					result.more = !queue.finished() || len(*pending) > 0
				}
				return
			}
		case <-queue.wake:
			if finishing != nil {
				continue
			}
		}
	}
}
