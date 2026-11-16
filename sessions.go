package kha

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// All lazily iterates all matching results.
func (c *SessionsClient) All(ctx context.Context, req ListSessionsRequest) iter.Seq2[*Session, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Session, *string, error) {
		request := req
		request.PageToken = token
		resp, err := c.List(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}

// AllEvents lazily iterates all matching results.
func (c *SessionsClient) AllEvents(ctx context.Context, req ListSessionEventsRequest) iter.Seq2[*SessionEvent, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*SessionEvent, *string, error) {
		request := req
		request.PageToken = token
		resp, err := c.ListEvents(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}

// AllResources lazily iterates all matching results.
func (c *SessionsClient) AllResources(ctx context.Context, req ListSessionResourcesRequest) iter.Seq2[*SessionResource, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*SessionResource, *string, error) {
		request := req
		request.PageToken = token
		resp, err := c.ListResources(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}

// CursorNow is the WithCursor value that receives only future events. The
// stream then opens with a control frame carrying the resume cursor.
const CursorNow = "now"

// SessionEventsIterator iterates decoded Session events; the terminal error,
// if any, is the last value yielded.
type SessionEventsIterator = iter.Seq2[*SessionEvent, error]

// EventsOption customizes SessionsClient.Events and SessionsClient.ThreadEvents.
type EventsOption func(*eventsConfig)

type eventsConfig struct {
	cursor     *string
	resume     bool
	deltas     bool
	noResync   bool // reconnect cursor=now streams verbatim, never backfill
	onRecovery func()
}

// WithCursor starts the stream after the given cursor (a previous frame's
// id, a SessionEventList.StreamCursor value, or CursorNow for live-only).
// Without it the stream replays the full retained history and then continues
// live.
func WithCursor(cursor string) EventsOption {
	return func(c *eventsConfig) { c.cursor = &cursor }
}

// WithResume toggles automatic recovery (default true). When enabled, a
// dropped stream reconnects from the last received frame id (sent to the
// server as Last-Event-ID; the generated layer never reconnects on its own), a
// clean end of the stream reconnects the same way, a 429/5xx on (re)connect
// earns one more attempt after the shared backoff, and an expired cursor
// triggers the spec's recovery flow: re-anchor with cursor=now, backfill
// the gap from event history, then continue live. When disabled, the first
// stream-ending error (including a clean EOF) is yielded verbatim and
// iteration stops.
func WithResume(enabled bool) EventsOption {
	return func(c *eventsConfig) { c.resume = enabled }
}

// WithDeltas toggles the stream-only agent.delta previews (default false,
// matching the API). Enable it for live typewriter-style rendering;
// consumers that only need complete events should leave it off. Previews
// are never persisted: an expired-cursor recovery discards them (see
// WithOnRecovery) and the eventual agent.message stays authoritative.
func WithDeltas(enabled bool) EventsOption {
	return func(c *eventsConfig) { c.deltas = enabled }
}

// WithOnRecovery registers a callback the iterator invokes each time an
// expired-cursor recovery completes: the stream has re-anchored on
// cursor=now, the gap has been backfilled from event history, and live
// delivery is about to resume. Being called means every agent.delta yielded
// BEFORE this point was a partial preview of a turn whose final content may
// differ — per the spec the consumer must discard those previews and treat
// the eventual agent.message as the authoritative form.
//
// The callback fires only at that completed-recovery point: a plain
// reconnect after a dropped stream does not trigger it, a recovery pass
// whose backfill fails does not trigger it, and it never fires with
// WithResume(false). Multiple completed recoveries call it once each.
//
// fn runs synchronously inside the iterator, between the backfilled events
// and the next live one; keep it cheap and non-blocking.
func WithOnRecovery(fn func()) EventsOption {
	return func(c *eventsConfig) { c.onRecovery = fn }
}

// Events streams the Session's events as a lazy iterator with the
// SDK's recovery state machine layered over the generated event stream:
//
//	for ev, err := range client.Sessions.Events(ctx, sessionID) {
//		if err != nil { ... } // terminal: iteration has stopped
//		switch ev.Type { case SessionEventTypeUserMessageEvent: ... }
//	}
//
// Semantics, all handled inside the iterator:
//
//   - Heartbeat comments (": hb") and the opening control frame are consumed
//     silently; only SessionEvent values are yielded. Event frames whose
//     event name this SDK's generated layer does not declare are skipped by the
//     generated layer and never surface here.
//   - Events are deduplicated by their payload id: resume and backfill may
//     re-deliver events, and ids are unique but not ordered. The dedupe
//     window holds the 4096 most recent ids. Events carrying an empty
//     payload id cannot be deduplicated and are always yielded.
//   - A dropped connection reconnects from the last received frame id, sent
//     as Last-Event-ID: the generated layer exposes the per-frame resume cursor
//     (LastEventID) but never reconnects on its own, so resume is entirely
//     the machine's. A clean end of the stream reconnects the same way, a
//     429/5xx on (re)connect earns one more attempt after the shared
//     backoff, and a stream that advanced without identifiable frames
//     re-syncs instead: the iterator backfills event history and resumes
//     live from the tip's stream cursor. Reconnects wait 1s→2s→4s→… (capped
//     at 8s, a 429 raising the wait to the server's Retry-After), unless
//     WithResume(false).
//   - An expired cursor (400 invalid_parameter_error, param=cursor) runs
//     the spec's recovery flow: re-anchor with cursor=now, backfill the gap
//     from event history, then continue the same anchored live connection.
//   - A terminal "event: error" frame is decoded into a *StreamError
//     and yielded as the iterator's last error;
//     iteration then stops.
//   - Cancelling ctx stops the iteration cleanly: no error is yielded for
//     the cancellation itself.
//
// Delivery order is stream order; after a re-sync or recovery the
// backfilled (older) events are yielded before the live stream resumes, so
// consumers that care about total order should sort by ProcessedAt.
//
// The default stream carries complete events only; WithDeltas(true) adds
// the stream-only agent.delta previews. Those previews are partial views of
// a turn whose final content may differ; per the spec, consumers must
// discard them once the stream re-anchors after an expired-cursor recovery
// and treat the eventual agent.message as the authoritative form.
// WithOnRecovery delivers that discard signal; a plain reconnect after a
// dropped stream loses nothing and fires nothing.
//
// The iterator never buffers the stream wholesale and never retries a
// handed-off response body; all recovery happens between connections.
func (c *SessionsClient) Events(ctx context.Context, sessionID string, opts ...EventsOption) SessionEventsIterator {
	cfg := newEventsConfig(opts)
	return streamEvents(ctx, c.eventSource(sessionID, cfg), cfg)
}

// ThreadEvents streams the Thread's events as a lazy iterator, the
// per-Thread counterpart of SessionsClient.Events sharing the same recovery
// state machine.
func (c *SessionsClient) ThreadEvents(ctx context.Context, sessionID, threadID string, opts ...EventsOption) SessionEventsIterator {
	cfg := newEventsConfig(opts)
	return streamEvents(ctx, c.threadEventSource(sessionID, threadID, cfg), cfg)
}

// newEventsConfig applies opts over the defaults.
func newEventsConfig(opts []EventsOption) eventsConfig {
	cfg := eventsConfig{resume: true}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// streamEvents builds the lazy StreamEvents iterator over one endpoint pair
// (shared by SessionsClient.Events and SessionsClient.ThreadEvents).
func streamEvents(ctx context.Context, src eventSource, cfg eventsConfig) SessionEventsIterator {
	return func(yield func(*SessionEvent, error) bool) {
		st := &eventStream{
			src:   src,
			cfg:   cfg,
			yield: yield,
			seen:  newIDSet(dedupeWindow),
		}
		st.run(ctx)
	}
}

// eventSource abstracts the two SSE endpoint pairs the stream machine
// drives: the Session's coordinator stream and any Thread's stream. The
// frame format, resume behavior, and history model match on both (per the
// spec), so one state machine serves both.
type eventSource struct {
	// stream opens one SSE connection through the generated handshake. cursor,
	// when non-nil and non-empty, is the query cursor; lastEventID, when
	// non-nil and non-empty, goes out as the Last-Event-ID header for an
	// explicit resume after a drop (the server gives cursor precedence
	// when both are set, so the machine never sends both).
	stream func(ctx context.Context, cursor, lastEventID *string) eventConn
	// history fetches one page of retained events, oldest first, the token
	// of the next page (nil when exhausted), and the page's stream cursor
	// for resuming live delivery (nil when the server omitted it).
	history func(ctx context.Context, pageToken *string) (items []*SessionEvent, next, tip *string, err error)
}

// eventConn is one open SSE connection: the decoded envelope stream, the
// last received frame id (the resume cursor for the next open), and the
// close hook.
type eventConn struct {
	items  iter.Seq2[any, error]
	lastID func() string
	close  func()
}

// errorConn returns an eventConn whose stream yields err once and ends, so
// a failed handshake rides the machine's ordinary error classification
// instead of a special case at every open site.
func errorConn(err error) eventConn {
	return eventConn{
		items:  func(yield func(any, error) bool) { yield(nil, err) },
		lastID: func() string { return "" },
		close:  func() {},
	}
}

// streamConn adapts one generated SSE iterator to the machine's connection
// shape. The Session and Thread stream families share it through the
// constraint: both iterators expose All, LastEventID, and Close over their
// own envelope type.
func streamConn[T any, I interface {
	All() iter.Seq2[T, error]
	LastEventID() string
	Close() error
}](it I) eventConn {
	return eventConn{
		items: func(yield func(any, error) bool) {
			for ev, err := range it.All() {
				if !yield(ev, err) {
					return
				}
			}
		},
		lastID: it.LastEventID,
		close:  func() { _ = it.Close() },
	}
}

// eventSource returns the endpoint pair of the Session's own event stream.
// cfg.deltas rides on every (re)open as the include_deltas opt-in.
func (c *SessionsClient) eventSource(sessionID string, cfg eventsConfig) eventSource {
	return eventSource{
		stream: func(ctx context.Context, cursor, lastEventID *string) eventConn {
			req := StreamSessionEventsRequest{
				ID:          sessionID,
				Cursor:      optionalString(cursor),
				LastEventID: optionalString(lastEventID),
			}
			if cfg.deltas {
				req.IncludeDeltas = new(true)
			}
			resp, err := c.StreamEvents(ctx, req)
			if err != nil {
				return errorConn(err)
			}
			return streamConn(resp.Body)
		},
		history: func(ctx context.Context, pageToken *string) ([]*SessionEvent, *string, *string, error) {
			req := ListSessionEventsRequest{ID: sessionID, PageToken: optionalString(pageToken)}
			resp, err := c.ListEvents(ctx, req)
			if err != nil {
				return nil, nil, nil, err
			}
			page := resp.Body
			return page.Items, optionalString(page.NextPageToken), optionalString(page.StreamCursor), nil
		},
	}
}

// threadEventSource returns the endpoint pair of the Thread's own event stream,
// the per-Thread counterpart of SessionsClient.eventSource.
func (c *SessionsClient) threadEventSource(sessionID, threadID string, cfg eventsConfig) eventSource {
	return eventSource{
		stream: func(ctx context.Context, cursor, lastEventID *string) eventConn {
			req := StreamThreadEventsRequest{
				ID:          sessionID,
				ThreadID:    threadID,
				Cursor:      optionalString(cursor),
				LastEventID: optionalString(lastEventID),
			}
			if cfg.deltas {
				req.IncludeDeltas = new(true)
			}
			resp, err := c.StreamThreadEvents(ctx, req)
			if err != nil {
				return errorConn(err)
			}
			return streamConn(resp.Body)
		},
		history: func(ctx context.Context, pageToken *string) ([]*SessionEvent, *string, *string, error) {
			req := ListThreadEventsRequest{ID: sessionID, ThreadID: threadID, PageToken: optionalString(pageToken)}
			resp, err := c.ListThreadEvents(ctx, req)
			if err != nil {
				return nil, nil, nil, err
			}
			page := resp.Body
			return page.Items, optionalString(page.NextPageToken), optionalString(page.StreamCursor), nil
		},
	}
}

// optionalString normalizes absent and empty cursors to nil.
func optionalString(o *string) *string {
	if o != nil && *o != "" {
		return o
	}
	return nil
}

// dedupeWindow bounds the id set used for duplicate suppression.
const dedupeWindow = 4096

// Reconnect backoff: 1s, 2s, 4s, capped at 8s. The escalation persists
// across reconnects and resets only once a stream lives past
// reconnectAliveThreshold; reconnectAliveThreshold bounds "healthy":
// a connection that dies below it (a gateway that accepts then closes)
// escalates instead of resetting, so a fleet cannot hammer-loop at 1 RPS.
const (
	reconnectBaseBackoff    = time.Second
	reconnectMaxBackoff     = 8 * time.Second
	reconnectAliveThreshold = 2 * time.Second
	reconnectJitterFraction = 0.25
)

// jitterWait scatters a reconnect wait by ±reconnectJitterFraction so
// synchronized recoveries do not pile onto the server at one instant.
var jitterWait = func(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	delta := 1 + (rand.Float64()*2-1)*reconnectJitterFraction
	return time.Duration(float64(d) * delta)
}

// pumpBuffer bounds the live-event buffer of a recovery pump: while the
// machine backfills history, the cursor=now stream keeps flowing into this
// channel; a full channel applies backpressure to the connection.
const pumpBuffer = 64

// streamOutcome classifies how a frame loop or recovery ended.
type streamOutcome int

const (
	outOK        streamOutcome = iota // phase done, keep going
	outEnded                          // stream ended cleanly (EOF), not via ctx
	outDropped                        // a transport drop: resume from the last received frame id
	outTransient                      // a transient (429/5xx) connect failure
	outExpired                        // the cursor's retained position was reclaimed
	outStopped                        // the consumer stopped the iteration
	outCanceled                       // ctx was canceled; end silently
	outTerminal                       // a terminal error was already yielded
)

// streamItem is one pull result from a generated stream iterator.
type streamItem struct {
	value any
	err   error
}

// itemSource abstracts how the machine pulls stream items: directly from a
// generated iterator, or through a recovery pump's buffer channel. lastEventID
// exposes the underlying connection's resume cursor — "" until a frame with
// an id arrives, and permanently for a source whose handshake failed.
type itemSource interface {
	next() (streamItem, bool) // ok=false: the source ended
	lastEventID() string
	stop()
}

// directSource pulls a generated iterator in the consumer's own goroutine.
type directSource struct {
	nextFn func() (any, error, bool)
	lastFn func() string
	stopFn func()
}

func (d *directSource) next() (streamItem, bool) {
	v, err, ok := d.nextFn()
	if !ok {
		return streamItem{}, false
	}
	return streamItem{value: v, err: err}, true
}

func (d *directSource) lastEventID() string { return d.lastFn() }

func (d *directSource) stop() { d.stopFn() }

// pumpSource pulls a generated iterator in a background goroutine, buffering
// items so the machine can backfill history while the re-anchored stream
// stays open (the spec's expired-cursor recovery order). stop is idempotent
// and also unblocks a goroutine parked inside next via ctx cancellation.
type pumpSource struct {
	ch     chan streamItem
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
	conn   atomic.Pointer[eventConn]
}

func (p *pumpSource) next() (streamItem, bool) {
	item, ok := <-p.ch
	return item, ok
}

// lastEventID reports the pumped connection's resume cursor: "" until the
// handshake lands, and permanently for a failed one.
func (p *pumpSource) lastEventID() string {
	if c := p.conn.Load(); c != nil {
		return c.lastID()
	}
	return ""
}

func (p *pumpSource) stop() {
	p.once.Do(func() { close(p.done) })
	p.cancel()
}

// eventStream holds the mutable state of one StreamEvents iteration.
type eventStream struct {
	src   eventSource
	cfg   eventsConfig
	yield func(*SessionEvent, error) bool

	seen *idSet
}

// run drives the connect → consume → reconnect state machine until the
// consumer stops, ctx is canceled, or a terminal error is yielded.
func (st *eventStream) run(ctx context.Context) {
	var wait time.Duration
	cursor := st.cfg.cursor // the current connection's start cursor (nil: full replay)
	var resumeID *string    // the last received frame id, the explicit resume cursor after a drop
	src := st.connect(ctx, cursor, nil)
	openedAt := time.Now()
	for {
		out, delivered, err := st.consume(ctx, src)
		if id := src.lastEventID(); id != "" {
			resumeID = new(id)
		}
		src.stop()
		switch out {
		case outStopped, outCanceled, outTerminal:
			return
		case outExpired:
			// The retained stream position was reclaimed. The spec's
			// recovery flow runs inside recoverExpiredCursor and hands back
			// the re-anchored stream; its later failures re-enter the
			// ordinary classification above.
			src2, cursor2, rout := st.recoverExpiredCursor(ctx)
			if rout != outOK {
				return
			}
			src, cursor, resumeID = src2, cursor2, nil
			openedAt = time.Now()
			wait = 0
			continue
		case outTransient:
			wait = transientWait(wait, err)
		case outDropped, outEnded:
			if !st.cfg.resume {
				st.yield(nil, err) // the drop or the io.EOF of a clean close, verbatim
				return
			}
			// A connection that dies almost immediately after opening is a
			// red flag (a gateway that accepts then closes): escalate rather
			// than reset, or the whole fleet hammer-loops at 1 RPS.
			if time.Since(openedAt) < reconnectAliveThreshold {
				wait = nextBackoff(wait)
			} else {
				wait = reconnectBaseBackoff
			}
			wait = jitterWait(wait)
		}
		if !sleepCtx(ctx, wait) {
			return
		}
		src2, cursor2, rout := st.reconnect(ctx, cursor, resumeID, delivered)
		if rout != outOK {
			return // the re-sync stopped, was canceled, or yielded a terminal error
		}
		src, cursor = src2, cursor2
		openedAt = time.Now()
		// The backoff state deliberately survives the reconnect: resetting it
		// here would make every flash-death loop compute nextBackoff from 0
		// and hammer at 1 RPS forever. Healthy connections reset via the
		// reconnectAliveThreshold branch above.
	}
}

// connect opens one stream through the generated handshake, pulling it directly.
// lastEventID, when set, goes out as the Last-Event-ID header for an
// explicit resume; the server gives the query cursor precedence, so the
// two never travel together.
func (st *eventStream) connect(ctx context.Context, cursor, lastEventID *string) itemSource {
	cctx, cancel := context.WithCancel(ctx)
	conn := st.src.stream(cctx, cursor, lastEventID)
	next, stop := iter.Pull2(conn.items)
	return &directSource{
		nextFn: next,
		lastFn: conn.lastID,
		stopFn: func() { stop(); conn.close(); cancel() },
	}
}

// pumpConnect opens one stream through the generated handshake behind a pump
// goroutine, so the subscription is established while the machine does
// other work (the history backfill of a recovery).
func (st *eventStream) pumpConnect(ctx context.Context, cursor *string) itemSource {
	pctx, cancel := context.WithCancel(ctx)
	p := &pumpSource{ch: make(chan streamItem, pumpBuffer), done: make(chan struct{}), cancel: cancel}
	ready := make(chan struct{})
	go func() {
		defer close(p.ch)
		defer cancel()
		conn := st.src.stream(pctx, cursor, nil)
		p.conn.Store(&conn)
		close(ready)
		defer conn.close()
		next, stop := iter.Pull2(conn.items)
		defer stop()
		for {
			v, err, ok := next()
			if !ok {
				return
			}
			select {
			case p.ch <- streamItem{value: v, err: err}:
			case <-p.done:
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		p.stop()
	}
	return p
}

// consume pulls the items of one open stream until it ends, the consumer
// stops, or an error requires a decision from run. delivered counts the
// event frames the connection produced (before dedupe), which run uses to
// tell an unmoved position from an advanced one.
func (st *eventStream) consume(ctx context.Context, src itemSource) (out streamOutcome, delivered int, err error) {
	for {
		item, ok := src.next()
		if ctx.Err() != nil {
			return outCanceled, delivered, nil
		}
		if !ok {
			return outEnded, delivered, io.EOF
		}
		if item.err != nil {
			switch st.classify(item.err) {
			case outExpired:
				return outExpired, delivered, nil
			case outTransient:
				return outTransient, delivered, item.err
			case outDropped:
				return outDropped, delivered, item.err
			default:
				st.yield(nil, item.err)
				return outTerminal, delivered, nil
			}
		}
		ev, derr := sessionEventFromStreamItem(item.value)
		if derr != nil {
			st.yield(nil, derr)
			return outTerminal, delivered, nil
		}
		if ev == nil {
			continue
		}
		delivered++
		// Dedupe by payload id; an empty id marks an event the server did
		// not identify, which cannot be deduplicated and is always
		// delivered.
		if id := SessionEventID(ev); id != "" && st.seen.dup(id) {
			continue
		}
		if !st.yield(ev, nil) {
			return outStopped, delivered, nil
		}
	}
}

// classify maps a stream failure to the machine outcome its kind deserves.
// WithResume(false) leaves every failure terminal, yielded verbatim.
func (st *eventStream) classify(err error) streamOutcome {
	if !st.cfg.resume {
		return outTerminal
	}
	status, _, isHTTP := httpErrorMetadata(err)
	switch {
	case isExpiredCursorError(err):
		if st.cfg.noResync {
			// A live-only wait cannot attribute historical events to its turn.
			// Preserve the rejection instead of backfilling or skipping the gap.
			return outTerminal
		}
		// The retained position was reclaimed: run the spec's recovery flow.
		return outExpired
	case isHTTP && (status == http.StatusTooManyRequests || status >= 500):
		// The product transport excludes SSE requests from retries; the
		// iterator retries these (re)connect rejections after its own backoff.
		return outTransient
	case IsProtocolError(err):
		// A deterministic generated decode failure would fail the same way when
		// a reconnect re-delivered the frame.
		return outTerminal
	case !isHTTP || isResponseReadError(err):
		// A transport drop: the generated layer never reconnects on its own, so
		// the machine resumes from the last received frame id.
		return outDropped
	default:
		// Other HTTP failures are terminal. Error frames are handled by consume.
		return outTerminal
	}
}

// reconnect picks the stream that replaces one that ended without an
// expired cursor: an explicit resume from the last received frame id when
// the connection produced one (sent as Last-Event-ID, no query cursor),
// a verbatim reconnect when the old connection's position never advanced
// (nothing was delivered on it), otherwise a re-sync that backfills event
// history and resumes live from the tip — the fallback for a stream that
// advanced without identifiable frames. noResync pins cursor=now streams
// away from the history round trip (SendMessageAndWait's live-only wait
// must never surface pre-subscribe history); a frame-id resume is safe
// there because the id only exists once the stream passed its anchor.
func (st *eventStream) reconnect(ctx context.Context, prev, resumeID *string, delivered int) (itemSource, *string, streamOutcome) {
	if resumeID != nil {
		return st.connect(ctx, nil, resumeID), nil, outOK
	}
	if delivered == 0 {
		return st.connect(ctx, prev, nil), prev, outOK
	}
	if st.cfg.noResync && prev != nil && *prev == CursorNow {
		return st.connect(ctx, prev, nil), prev, outOK
	}
	return st.resync(ctx, false, prev)
}

// recoverExpiredCursor subscribes at cursor=now before backfilling history, then continues the same live connection. Overlap is deduplicated by payload ID.
func (st *eventStream) recoverExpiredCursor(ctx context.Context) (itemSource, *string, streamOutcome) {
	src, cursor, out := st.resync(ctx, true, nil)
	if out != outOK {
		return nil, nil, out
	}
	if st.cfg.onRecovery != nil {
		// The recovery is complete — re-anchored and backfilled, live
		// delivery about to resume. Anything delta-streamed before this
		// point was a partial preview the consumer must now discard.
		st.cfg.onRecovery()
	}
	return src, cursor, outOK
}

// resync optionally establishes a live subscription before backfilling. Without an anchored connection, it resumes from the history tip.
func (st *eventStream) resync(ctx context.Context, anchorFirst bool, fallbackCursor *string) (itemSource, *string, streamOutcome) {
	var pump itemSource
	if anchorFirst {
		pump = st.pumpConnect(ctx, new(CursorNow))
	}
	tip, out := st.backfill(ctx)
	if out != outOK {
		if pump != nil {
			pump.stop()
		}
		return nil, nil, out
	}
	if pump != nil {
		// Until a frame supplies a cursor, reconnect from the history tip.
		// A refused or empty live connection must not reopen at a newer "now".
		return pump, tip, outOK
	}
	if tip != nil {
		return st.connect(ctx, tip, nil), tip, outOK
	}
	return st.connect(ctx, fallbackCursor, nil), fallbackCursor, outOK
}

// backfill closes the history gap before live delivery resumes, returning
// the last page's stream cursor. Transient failures retry the current page
// until it succeeds or the caller cancels.
func (st *eventStream) backfill(ctx context.Context) (*string, streamOutcome) {
	var token, tip *string
	seen := make(map[string]bool)
	var wait time.Duration
	for {
		items, next, pageTip, err := st.src.history(ctx, token)
		if err != nil {
			if ctx.Err() != nil {
				return nil, outCanceled
			}
			if !isTransientStreamError(err) {
				st.yield(nil, err)
				return nil, outTerminal
			}
			wait = transientWait(wait, err)
			if !sleepCtx(ctx, wait) {
				return nil, outCanceled
			}
			continue
		}
		wait = 0
		if pageTip != nil {
			tip = pageTip
		}
		for _, ev := range items {
			if ev == nil {
				continue
			}
			if id := SessionEventID(ev); id != "" && st.seen.dup(id) {
				continue
			}
			if !st.yield(ev, nil) {
				return nil, outStopped
			}
		}
		if next == nil {
			return tip, outOK
		}
		if seen[*next] {
			st.yield(nil, errors.New("kha: pagination token did not advance"))
			return nil, outTerminal
		}
		seen[*next] = true
		token = next
	}
}

// sessionEventFromStreamItem unwraps typed frame data without re-encoding JSON.
func sessionEventFromStreamItem(v any) (*SessionEvent, error) {
	switch frame := v.(type) {
	case *StreamControlFrame:
		return nil, nil
	case *APIErrorFrame:
		if frame != nil && frame.Data != nil {
			return nil, &StreamError{Details: frame.Data.Error}
		}
	case *AgentCustomToolUseFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentCustomToolUseEvent(frame.Data), nil
		}
	case *AgentDeltaFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentDeltaEvent(frame.Data), nil
		}
	case *AgentMessageFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentMessageEvent(frame.Data), nil
		}
	case *AgentMessageStartedFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentMessageStartedEvent(frame.Data), nil
		}
	case *AgentModelStartedFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentModelStartedEvent(frame.Data), nil
		}
	case *AgentThinkingFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentThinkingEvent(frame.Data), nil
		}
	case *AgentThinkingStartedFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentThinkingStartedEvent(frame.Data), nil
		}
	case *AgentThreadContextCompactedFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentThreadContextCompactedEvent(frame.Data), nil
		}
	case *AgentThreadMessageReceivedFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentThreadMessageReceivedEvent(frame.Data), nil
		}
	case *AgentThreadMessageSentFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentThreadMessageSentEvent(frame.Data), nil
		}
	case *AgentToolResultFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentToolResultEvent(frame.Data), nil
		}
	case *AgentToolStartedFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentToolStartedEvent(frame.Data), nil
		}
	case *AgentToolUseFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventAgentToolUseEvent(frame.Data), nil
		}
	case *SessionErrorFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventSessionErrorEvent(frame.Data), nil
		}
	case *SessionStatusFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventSessionStatusEvent(frame.Data), nil
		}
	case *SessionThreadCreatedFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventSessionThreadCreatedEvent(frame.Data), nil
		}
	case *SessionThreadStatusFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventSessionThreadStatusEvent(frame.Data), nil
		}
	case *SessionTitleUpdatedFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventSessionTitleUpdatedEvent(frame.Data), nil
		}
	case *SessionUpdatedFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventSessionUpdatedEvent(frame.Data), nil
		}
	case *SessionUsageFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventSessionUsageEvent(frame.Data), nil
		}
	case *UserCustomToolResultFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventUserCustomToolResultEvent(frame.Data), nil
		}
	case *UserInterruptFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventUserInterruptEvent(frame.Data), nil
		}
	case *UserMessageFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventUserMessageEvent(frame.Data), nil
		}
	case *UserToolConfirmationFrame:
		if frame != nil && frame.Data != nil {
			return NewSessionEventUserToolConfirmationEvent(frame.Data), nil
		}
	}
	return nil, &InvalidCallError{Reason: fmt.Sprintf("unexpected stream event %T", v)}
}

// SessionEventID returns the payload id used for duplicate suppression: the
// server-assigned event identifier of the union's selected variant, or "" for
// variants unknown to this SDK and events the server did not identify — both
// are never deduplicated and always delivered.
func SessionEventID(ev *SessionEvent) string {
	switch ev.Type {
	case SessionEventTypeUserMessageEvent:
		if v := ev.UserMessageEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeUserInterruptEvent:
		if v := ev.UserInterruptEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeUserToolConfirmationEvent:
		if v := ev.UserToolConfirmationEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeUserCustomToolResultEvent:
		if v := ev.UserCustomToolResultEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentMessageEvent:
		if v := ev.AgentMessageEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentThinkingEvent:
		if v := ev.AgentThinkingEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentToolUseEvent:
		if v := ev.AgentToolUseEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentToolResultEvent:
		if v := ev.AgentToolResultEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentModelStartedEvent:
		if v := ev.AgentModelStartedEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentThinkingStartedEvent:
		if v := ev.AgentThinkingStartedEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentMessageStartedEvent:
		if v := ev.AgentMessageStartedEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentToolStartedEvent:
		if v := ev.AgentToolStartedEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentCustomToolUseEvent:
		if v := ev.AgentCustomToolUseEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentThreadMessageReceivedEvent:
		if v := ev.AgentThreadMessageReceivedEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentThreadMessageSentEvent:
		if v := ev.AgentThreadMessageSentEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentThreadContextCompactedEvent:
		if v := ev.AgentThreadContextCompactedEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeAgentDeltaEvent:
		if v := ev.AgentDeltaEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeSessionStatusEvent:
		if v := ev.SessionStatusEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeSessionErrorEvent:
		if v := ev.SessionErrorEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeSessionThreadCreatedEvent:
		if v := ev.SessionThreadCreatedEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeSessionThreadStatusEvent:
		if v := ev.SessionThreadStatusEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeSessionTitleUpdatedEvent:
		if v := ev.SessionTitleUpdatedEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeSessionUpdatedEvent:
		if v := ev.SessionUpdatedEvent; v != nil {
			return v.ID
		}
	case SessionEventTypeSessionUsageEvent:
		if v := ev.SessionUsageEvent; v != nil {
			return v.ID
		}
	}
	return ""
}

// errorTypeInvalidParameter is the error envelope type of a rejected
// parameter; the expired-cursor rejection uses it with param=cursor.
const errorTypeInvalidParameter = "invalid_parameter_error"

// isExpiredCursorError reports whether err is the "cursor expired" rejection
// the spec's recovery flow exists for: 400 invalid_parameter_error with
// param=cursor whose message says the cursor expired. (A param=cursor error
// without "expired" marks an unusable cursor the caller must fix; it is
// surfaced, not recovered.)
func isExpiredCursorError(e error) bool {
	details, ok := ErrorDetails(e)
	if !ok {
		return false
	}
	return IsInvalidRequest(e) &&
		string(details.Type) == errorTypeInvalidParameter &&
		details.Param != nil && *details.Param == "cursor" &&
		strings.Contains(strings.ToLower(details.Message), "expired")
}

// isTransientStreamError reports whether a history fetch failure is worth
// retrying inside stream recovery: a network error, a 429, or any 5xx. The
// expired-cursor 400 is classified before this is consulted; every other
// 4xx is terminal, and so is any protocol error (a caller-side violation or
// a 2xx page that failed to decode — the peer's bytes are deterministic and
// retrying the same fetch can only livelock).
func isTransientStreamError(err error) bool {
	if IsProtocolError(err) {
		return false
	}
	status, _, isHTTP := httpErrorMetadata(err)
	return !isHTTP || isResponseReadError(err) || status == http.StatusTooManyRequests || status >= 500
}

// transientWait advances the shared reconnect and history-fetch backoff
// after one transient failure; on a 429 the wait rises to Retry-After.
func transientWait(wait time.Duration, err error) time.Duration {
	wait = nextBackoff(wait)
	if status, header, ok := httpErrorMetadata(err); ok && status == http.StatusTooManyRequests {
		wait = max(retryAfter(header.Get("Retry-After")), wait)
	}
	return wait
}

// nextBackoff advances the reconnect backoff: 1s, 2s, 4s, capped at 8s.
func nextBackoff(wait time.Duration) time.Duration {
	if wait == 0 {
		return reconnectBaseBackoff
	}
	if wait *= 2; wait > reconnectMaxBackoff {
		wait = reconnectMaxBackoff
	}
	return wait
}

// idSet is a bounded insertion-ordered set of event ids for duplicate
// suppression. The bound keeps memory flat on month-long streams; eviction
// drops the oldest ids, which a re-delivery would have to exceed the whole
// window to collide with.
type idSet struct {
	seen  map[string]struct{}
	order []string
	cap   int
}

func newIDSet(capacity int) *idSet {
	return &idSet{seen: make(map[string]struct{}), cap: capacity}
}

// dup reports whether id was already recorded, recording it when new.
func (s *idSet) dup(id string) bool {
	if _, ok := s.seen[id]; ok {
		return true
	}
	if len(s.order) == s.cap {
		delete(s.seen, s.order[0])
		s.order = s.order[1:]
	}
	s.seen[id] = struct{}{}
	s.order = append(s.order, id)
	return false
}

// TurnResult summarizes a turn that ended while SendMessageAndWait watched.
type TurnResult struct {
	// StopReason is why the turn stopped (StopReasonEndTurn,
	// StopReasonCanceled, StopReasonInternalError), taken from the
	// session.status event that returned the Session to idle. Empty when the
	// server omitted it.
	StopReason StopReason `json:"stop_reason"`
	// LastEvent is the terminal event: the session.status transition to
	// idle that ended the wait.
	LastEvent *SessionEvent `json:"last_event"`
}

// SessionError is a failed Session run reported by a session.error event.
// It is a different vocabulary from APIError: it never travels over
// HTTP, its Type is a SessionErrorType (open vocabulary), and it
// surfaces from SessionsClient.SendMessageAndWait.
type SessionError struct {
	// Type is the machine-readable failure category (open vocabulary).
	Type SessionErrorType `json:"type"`
	// Message is the human-readable failure description.
	Message string `json:"message"`
	// EventID identifies the session.error event in the Session history.
	EventID string `json:"event_id"`
}

// Error implements error.
func (e *SessionError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "kha: session run failed: %s", e.Type)
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	return b.String()
}

// SendMessageAndWait sends a user message and blocks until the turn it
// started ends — the Session's status returns to idle — reporting the
// outcome as a TurnResult. It is the SDK's standard "ask and await the
// answer" path and must not be reassembled per caller.
//
// How the turn is attributed to this call: when the deployment serves an
// input queue, the send response carries an event_id that the consumption
// echo reuses as its event id; the wait subscribes with full-history replay
// and starts watching only once that echo appears, so status transitions
// from earlier turns never end it prematurely. When the response carries no
// event_id, the wait subscribes live-only (cursor=now) from just after the
// send and never backfills on reconnects (pre-subscribe history must not
// end the wait). An expired resume cursor returns the server's error because
// the turn can no longer be tracked safely. A turn that completes inside the
// subscribe window of such a deployment is not observed (it is reported as a timeout), so callers
// there should prefer polling Sessions.Get.
//
// A session.error event ends the wait with a *SessionError, except for
// repository provisioning diagnostics. Those arrive independently of the
// model turn, so the wait continues until session.status reports the turn's
// actual outcome. A stream or API failure ends it with the underlying error
// (a typed ResponseError or UnexpectedResponseError). Cancelling
// ctx, or the WithTimeout deadline (default 10 minutes, counted from when
// the message was accepted), ends it with ctx's error. WithInterval has no
// effect here: the wait is event-driven, never polled. Events
// resume/dedupe/backfill follows SessionsClient.Events semantics.
func isRepositoryProvisioningError(t SessionErrorType) bool {
	switch t {
	case SessionErrorTypeRepositoryAuthenticationError,
		SessionErrorTypeRepositoryCheckoutError,
		SessionErrorTypeRepositoryCloneError,
		SessionErrorTypeRepositoryForbiddenError,
		SessionErrorTypeRepositoryNotFoundError:
		return true
	default:
		return false
	}
}

func (c *SessionsClient) SendMessageAndWait(ctx context.Context, sessionID string, text string, opts ...WaitOption) (*TurnResult, error) {
	payload := UserMessagePayload{}
	if text != "" {
		payload.Content = []*ContentBlock{
			NewContentBlockTextContentBlock(&TextContentBlock{
				Type: TextContentBlockTypeText,
				Text: text,
			}),
		}
	}
	input := NewUserEventInputUserMessageInput(&UserMessageInput{
		Type: UserMessageInputTypeUserMessage,
		Data: payload,
	})
	return c.SendEventAndWait(ctx, sessionID, input, opts...)
}

// SendEventAndWait is SendMessageAndWait for a caller-built user event input
// (rich content blocks, file references): it sends the event and applies the
// same turn attribution and outcome rules. The input must be a queued user
// event (user.message); interrupt and tool-result inputs have their own
// semantics and are not turn starters.
func (c *SessionsClient) SendEventAndWait(ctx context.Context, sessionID string, input *UserEventInput, opts ...WaitOption) (*TurnResult, error) {
	cfg := newWaitConfig(opts)
	resp, err := c.CreateEvents(ctx, CreateSessionEventsRequest{
		ID:   sessionID,
		Body: &SendEventsRequest{Events: []*UserEventInput{input}},
	})
	if err != nil {
		return nil, err
	}
	ack := resp.Body

	wctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	echoID := ack.EventID
	hasEcho := echoID != nil && *echoID != ""
	armed := !hasEcho // without an echo id there is nothing to arm on
	eventCfg := newEventsConfig(nil)
	if !hasEcho {
		eventCfg = newEventsConfig([]EventsOption{WithCursor(CursorNow)}) // live-only tail instead
		eventCfg.noResync = true                                          // and never backfill pre-subscribe history
	}

	for ev, err := range streamEvents(wctx, c.eventSource(sessionID, eventCfg), eventCfg) {
		if err != nil {
			return nil, err
		}
		if !armed {
			if m := ev.UserMessageEvent; ev.Type == SessionEventTypeUserMessageEvent && m != nil && m.ID == *echoID {
				armed = true
			}
			continue
		}
		if se := ev.SessionErrorEvent; ev.Type == SessionEventTypeSessionErrorEvent && se != nil && !isRepositoryProvisioningError(se.Data.ErrorType) {
			return nil, &SessionError{
				Type:    se.Data.ErrorType,
				Message: se.Data.Message,
				EventID: se.ID,
			}
		}
		if st := ev.SessionStatusEvent; ev.Type == SessionEventTypeSessionStatusEvent && st != nil && st.Data.Status == SessionStateIdle {
			res := &TurnResult{LastEvent: ev}
			if sr := st.Data.StopReason; sr != nil {
				res.StopReason = sr.Type
			}
			return res, nil
		}
	}
	if err := wctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("kha: event stream ended before the turn completed")
}

// AllThreads lazily iterates all matching results.
func (c *SessionsClient) AllThreads(ctx context.Context, req ListSessionThreadsRequest) iter.Seq2[*SessionThread, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*SessionThread, *string, error) {
		request := req
		request.PageToken = token
		resp, err := c.ListThreads(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}

// AllThreadEvents lazily iterates all matching results.
func (c *SessionsClient) AllThreadEvents(ctx context.Context, req ListThreadEventsRequest) iter.Seq2[*SessionEvent, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*SessionEvent, *string, error) {
		request := req
		request.PageToken = token
		resp, err := c.ListThreadEvents(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}
