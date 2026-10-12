package javdb

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func newTestQueue(interval, queueTimeout, cooldown time.Duration) *requestQueue {
	q := newRequestQueue()
	q.interval, q.queueTimeout, q.cooldown = interval, queueTimeout, cooldown
	return q
}

func TestRequestQueue_SpacesConcurrentRequests(t *testing.T) {
	q := newTestQueue(50*time.Millisecond, time.Second, time.Second)

	var (
		mu    sync.Mutex
		times []time.Time
		wg    sync.WaitGroup
	)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := q.acquire(false); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(times) != 5 {
		t.Fatalf("got %d acquisitions, want 5", len(times))
	}
	first, last := times[0], times[0]
	for _, ts := range times {
		if ts.Before(first) {
			first = ts
		}
		if ts.After(last) {
			last = ts
		}
	}
	// 5 requests, 50ms apart: the last starts >= 200ms after the first.
	if span := last.Sub(first); span < 190*time.Millisecond {
		t.Errorf("requests not spaced out: span %v", span)
	}
}

func TestRequestQueue_FollowUpsGoFirst(t *testing.T) {
	q := newTestQueue(50*time.Millisecond, 5*time.Second, time.Second)

	var (
		mu    sync.Mutex
		order []string
		wg    sync.WaitGroup
	)
	acquire := func(name string, followUp bool) {
		defer wg.Done()
		if err := q.acquire(followUp); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}
	// Queue up four new searches, then a follow-up behind them.
	for _, name := range []string{"s1", "s2", "s3", "s4"} {
		wg.Add(1)
		go acquire(name, false)
	}
	time.Sleep(20 * time.Millisecond) // s1 is granted; s2..s4 wait
	wg.Add(1)
	go acquire("follow-up", true)
	wg.Wait()

	pos := map[string]int{}
	for i, name := range order {
		pos[name] = i
	}
	if pos["follow-up"] > 1 {
		t.Errorf("follow-up should jump the queue, got order %v", order)
	}
}

func TestRequestQueue_GivesUpWhenWaitTooLong(t *testing.T) {
	q := newTestQueue(time.Second, 100*time.Millisecond, time.Second)
	q.backoff() // queue paused for 1s, longer than the 100ms queue timeout

	start := time.Now()
	if err := q.acquire(false); !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("got %v, want ErrQueueTimeout", err)
	}
	if waited := time.Since(start); waited > 300*time.Millisecond {
		t.Errorf("gave up after %v, want ~100ms", waited)
	}
}

func TestRequestQueue_BackoffDelaysEveryone(t *testing.T) {
	q := newTestQueue(10*time.Millisecond, time.Second, 200*time.Millisecond)
	q.backoff()

	start := time.Now()
	if err := q.acquire(true); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 180*time.Millisecond {
		t.Errorf("waited %v, want ~200ms cooldown", waited)
	}
}
