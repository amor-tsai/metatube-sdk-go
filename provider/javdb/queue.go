package javdb

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults for the request queue; each can be overridden through provider
// config, e.g. MT_MOVIE_PROVIDER_JAVDB__INTERVAL=2s.
const (
	defaultInterval     = time.Second      // gap between two requests to javdb.com
	defaultQueueTimeout = 20 * time.Second // longest a request may wait for its turn
	defaultCooldown     = 10 * time.Second // pause for everyone after a 429/403
	queueCapacity       = 1024
)

var ErrQueueTimeout = errors.New("javdb: request queue is full, skipped to avoid rate limiting")

// requestQueue lets one request through to javdb.com per interval. JavDB
// answers bursts (e.g. a full Plex library scan searching hundreds of titles
// at once) with 429 Too Many Requests and then 403 Forbidden, and MetaTube
// searches every provider concurrently for every query, so without this the
// requests of many simultaneous searches would all hit JavDB at once.
//
// Two lanes, served by a single worker: follow-up requests of a search
// already in progress (login probe, movie page) go before new searches, so
// each search finishes as soon as possible instead of every search waiting
// for every other one. A request that can't get a turn within queueTimeout
// gives up: MetaTube waits for every provider before answering a search, and
// the Plex agent calling it has its own timeout.
type requestQueue struct {
	interval     time.Duration
	queueTimeout time.Duration
	cooldown     time.Duration

	start      sync.Once
	high, low  chan *ticket
	mu         sync.Mutex
	pauseUntil time.Time
}

type ticket struct {
	ready     chan struct{}
	abandoned atomic.Bool
}

func newRequestQueue() *requestQueue {
	return &requestQueue{
		interval:     defaultInterval,
		queueTimeout: defaultQueueTimeout,
		cooldown:     defaultCooldown,
		high:         make(chan *ticket, queueCapacity),
		low:          make(chan *ticket, queueCapacity),
	}
}

func (q *requestQueue) run() {
	for {
		q.mu.Lock()
		pause := time.Until(q.pauseUntil)
		q.mu.Unlock()
		if pause > 0 {
			time.Sleep(pause)
		}

		var t *ticket
		select {
		case t = <-q.high:
		default:
			select {
			case t = <-q.high:
			case t = <-q.low:
			}
		}
		if t.abandoned.Load() {
			continue // caller gave up; don't spend a slot on it
		}
		close(t.ready)
		time.Sleep(q.interval)
	}
}

// acquire blocks until it's this caller's turn, or returns ErrQueueTimeout.
// followUp marks requests that belong to a search already in progress.
func (q *requestQueue) acquire(followUp bool) error {
	q.start.Do(func() { go q.run() })

	t := &ticket{ready: make(chan struct{})}
	lane := q.low
	if followUp {
		lane = q.high
	}
	select {
	case lane <- t:
	default:
		return ErrQueueTimeout
	}

	timer := time.NewTimer(q.queueTimeout)
	defer timer.Stop()
	select {
	case <-t.ready:
		return nil
	case <-timer.C:
		t.abandoned.Store(true)
		select {
		case <-t.ready: // granted at the last moment
			return nil
		default:
			return ErrQueueTimeout
		}
	}
}

// backoff pauses the whole queue for the cooldown after JavDB pushed back,
// so every waiting request holds off, not just the one that got the 429.
func (q *requestQueue) backoff() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if until := time.Now().Add(q.cooldown); q.pauseUntil.Before(until) {
		q.pauseUntil = until
	}
}
