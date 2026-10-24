//go:build loadtest

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"slices"
	"strconv"
	"sync"
	"time"
)

// opStats counts the answers to one kind of request.
type opStats struct {
	Requests int            `json:"requests"`
	Status   map[string]int `json:"status"` // "200", "409 tag_cycle", "error" (transport)
	// Bulk changes: answers with updated > 0, and the sum of updated.
	Changed int     `json:"changed,omitempty"`
	Updated int64   `json:"updated,omitempty"`
	P50ms   float64 `json:"p50ms"`
	P99ms   float64 `json:"p99ms"`
	MaxMs   float64 `json:"maxMs"`
	lat     []time.Duration
}

// stats collects every recorded request of a scenario.
type stats struct {
	mu  sync.Mutex
	ops map[string]*opStats
}

func newStats() *stats { return &stats{ops: map[string]*opStats{}} }

func (s *stats) record(op string, status int, code string, d time.Duration, err error) {
	key := strconv.Itoa(status)
	switch {
	case err != nil:
		key = "error"
	case code != "":
		key += " " + code
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.ops[op]
	if o == nil {
		o = &opStats{Status: map[string]int{}}
		s.ops[op] = o
	}
	o.Requests++
	o.Status[key]++
	o.lat = append(o.lat, d)
}

func (s *stats) bulkUpdated(op string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.ops[op]; o != nil && n > 0 {
		o.Changed++
		o.Updated += int64(n)
	}
}

// finish computes the latency percentiles.
func (s *stats) finish() map[string]*opStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.ops {
		o.P50ms, o.P99ms, o.MaxMs = percentileMs(o.lat, 0.50), percentileMs(o.lat, 0.99), percentileMs(o.lat, 1)
	}
	return s.ops
}

// percentileMs is the nearest-rank percentile in milliseconds.
func percentileMs(lat []time.Duration, p float64) float64 {
	if len(lat) == 0 {
		return 0
	}
	s := slices.Clone(lat)
	slices.Sort(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	i = max(0, min(i, len(s)-1))
	return math.Round(float64(s[i])/float64(time.Microsecond)) / 1000
}

// transport is shared by all clients: one connection pool to the server.
var transport = &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 256, IdleConnTimeout: 90 * time.Second}

// client is one logged-in session of a test user.
type client struct {
	base string
	user *testUser
	hc   *http.Client
	st   *stats
}

// reply is a server answer: the status, the error code of a refusal, and
// the body.
type reply struct {
	status int
	code   string
	body   []byte
}

// login starts a session for u: one session per client, as several tabs or
// devices of the user would.
func login(ctx context.Context, base string, u *testUser, st *stats) (*client, error) {
	jar, _ := cookiejar.New(nil)
	c := &client{base: base, user: u, st: st, hc: &http.Client{Jar: jar, Transport: transport, Timeout: 10 * time.Minute}}
	r, err := c.do(ctx, "", http.MethodPost, "/api/session", map[string]string{"username": u.Name, "password": u.password})
	if err != nil {
		return nil, err
	}
	if r.status != http.StatusOK {
		return nil, fmt.Errorf("login %s: %d %s", u.Name, r.status, r.body)
	}
	return c, nil
}

// do sends a JSON request; op names it in the statistics ("" records
// nothing: setup and reads that are not part of the mix).
func (c *client) do(ctx context.Context, op, method, path string, body any) (reply, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return reply{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return reply{}, err
	}
	if method != http.MethodGet {
		// The server wants the JSON type on every state-changing request,
		// also a DELETE without a body.
		req.Header.Set("Content-Type", "application/json")
	}
	return c.send(op, req)
}

// upload sends a raw PDF to POST /api/documents.
func (c *client) upload(ctx context.Context, op, name string, data []byte) (reply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/documents", bytes.NewReader(data))
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("X-File-Name", name)
	return c.send(op, req)
}

func (c *client) send(op string, req *http.Request) (reply, error) {
	// The server takes state-changing requests only from its own origin.
	req.Header.Set("Origin", c.base)
	start := time.Now()
	resp, err := c.hc.Do(req)
	var r reply
	if err == nil {
		r.status = resp.StatusCode
		r.body, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if r.status >= 400 {
			var e struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(r.body, &e) == nil {
				r.code = e.Code
			}
		}
	}
	if op != "" {
		c.st.record(op, r.status, r.code, time.Since(start), err)
	}
	return r, err
}
