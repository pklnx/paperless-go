//go:build loadtest

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Review B's metadata: per user 6 top-level tags with 3 sub-tags each, 5
// correspondents and 5 document types, replenished while the mix deletes
// them.
const (
	wantRoots    = 6
	wantChildren = 3
	wantNamed    = 5
	maxRoots     = 12 // deleted parents make their children top-level tags
)

// metaCache is what a user's threads know of the user's metadata: filled
// from the API's answers and refreshed from the lists every second. It may
// be stale; the answers to stale IDs (404, owner_mismatch) are part of the
// mix, as in review B.
type metaCache struct {
	mu    sync.Mutex
	tags  map[string]*string // id → parent id
	corrs map[string]bool
	types map[string]bool
}

func newMetaCache() *metaCache {
	return &metaCache{tags: map[string]*string{}, corrs: map[string]bool{}, types: map[string]bool{}}
}

// refresh replaces the cache with the server's lists.
func (m *metaCache) refresh(ctx context.Context, c *client) error {
	var tags struct {
		Tags []struct {
			ID       string  `json:"id"`
			ParentID *string `json:"parentId"`
		} `json:"tags"`
	}
	var corrs struct {
		Correspondents []struct {
			ID string `json:"id"`
		} `json:"correspondents"`
	}
	var types struct {
		DocumentTypes []struct {
			ID string `json:"id"`
		} `json:"documentTypes"`
	}
	for _, l := range []struct {
		path string
		into any
	}{{"/api/tags", &tags}, {"/api/correspondents", &corrs}, {"/api/document-types", &types}} {
		r, err := c.do(ctx, "", http.MethodGet, l.path, nil)
		if err != nil {
			return err
		}
		if r.status != http.StatusOK {
			return fmt.Errorf("GET %s: %d", l.path, r.status)
		}
		if err := json.Unmarshal(r.body, l.into); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tags = map[string]*string{}
	for _, t := range tags.Tags {
		m.tags[t.ID] = t.ParentID
	}
	m.corrs = map[string]bool{}
	for _, x := range corrs.Correspondents {
		m.corrs[x.ID] = true
	}
	m.types = map[string]bool{}
	for _, x := range types.DocumentTypes {
		m.types[x.ID] = true
	}
	return nil
}

// view is a sorted copy of the cache, so that a seeded random choice does
// not depend on map order.
type view struct {
	tags, roots, corrs, types []string
	children                  map[string][]string // root → its sub-tags
	parent                    map[string]*string
}

func (m *metaCache) view() view {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := view{children: map[string][]string{}, parent: map[string]*string{}}
	for id, p := range m.tags {
		v.tags = append(v.tags, id)
		v.parent[id] = p
		if p == nil {
			v.roots = append(v.roots, id)
		}
	}
	for id, p := range m.tags {
		if p != nil && m.tags[*p] == nil {
			if _, ok := m.tags[*p]; ok {
				v.children[*p] = append(v.children[*p], id)
			}
		}
	}
	for id := range m.corrs {
		v.corrs = append(v.corrs, id)
	}
	for id := range m.types {
		v.types = append(v.types, id)
	}
	slices.Sort(v.tags)
	slices.Sort(v.roots)
	slices.Sort(v.corrs)
	slices.Sort(v.types)
	for _, c := range v.children {
		slices.Sort(c)
	}
	return v
}

func (m *metaCache) setTag(id string, parent *string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tags[id] = parent
}

func (m *metaCache) dropTag(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tags, id)
	for c, p := range m.tags {
		if p != nil && *p == id {
			m.tags[c] = nil // ON DELETE SET NULL: sub-tags become top-level
		}
	}
}

func (m *metaCache) setNamed(kind, id string, present bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.corrs
	if kind == "document_type" {
		set = m.types
	}
	if present {
		set[id] = true
	} else {
		delete(set, id)
	}
}

var nameSeq atomic.Int64

// newName is a name no other create or rename of this run uses.
func newName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, nameSeq.Add(1))
}

// mixWorker is one client thread of S1/S2.
type mixWorker struct {
	c    *client
	m    *metaCache
	rng  *rand.Rand
	flow string
}

// pick returns a random element, or "" for none.
func (w *mixWorker) pick(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[w.rng.IntN(len(s))]
}

// step sends one operation of review B's mix.
func (w *mixWorker) step(ctx context.Context) {
	switch r := w.rng.IntN(100); {
	case r < 30:
		w.bulk(ctx)
	case r < 65:
		w.patch(ctx)
	default:
		w.metadata(ctx)
	}
}

// bulk sends a bulk change by match or by IDs with one of review B's
// changes.
func (w *mixWorker) bulk(ctx context.Context) {
	v := w.m.view()
	body := map[string]any{}
	t, corr, typ := w.pick(v.tags), w.pick(v.corrs), w.pick(v.types)
	switch w.rng.IntN(8) {
	case 0:
		if t != "" {
			body["addTags"] = []string{t}
		}
	case 1:
		if t != "" {
			body["removeTags"] = []string{t}
		}
	case 2:
		if corr != "" {
			body["correspondent"] = corr
		}
	case 3:
		body["correspondent"] = nil
	case 4:
		if typ != "" {
			body["documentType"] = typ
		}
	case 5:
		body["documentType"] = nil
	case 6:
		if t != "" && corr != "" {
			body["addTags"], body["correspondent"] = []string{t}, corr
		}
	}
	if len(body) == 0 {
		body["favorite"] = w.rng.IntN(2) == 0
	}

	if w.rng.IntN(2) == 0 {
		ids := w.c.user.docIDs
		n := min(20+w.rng.IntN(481), len(ids))
		sel := make([]string, n)
		for i, j := range w.rng.Perm(len(ids))[:n] {
			sel[i] = ids[j]
		}
		body["ids"] = sel
		w.sendBulk(ctx, "bulk_ids", body)
		return
	}
	match := map[string]any{}
	switch w.rng.IntN(4) {
	case 0:
		match["inbox"] = true
	case 1:
		match["q"] = "invoice"
	case 2:
		if mt := w.pick(v.tags); mt != "" {
			match["tag"] = []string{mt}
		}
	}
	body["match"] = match
	if w.flow == flowFingerprint {
		fp, ok := dryRun(ctx, w.c, match)
		if !ok {
			return
		}
		if fp != "" {
			body["fingerprint"] = fp
		}
	}
	w.sendBulk(ctx, "bulk_match", body)
}

// dryRun counts a match as the UI does before a change by match and
// returns the answer's fingerprint ("" from a server before #106, or
// above the limit); ok is false when the dry run was refused.
func dryRun(ctx context.Context, c *client, match map[string]any) (string, bool) {
	r, err := c.do(ctx, "bulk_dry_run", http.MethodPost, "/api/documents/bulk", map[string]any{"match": match, "dryRun": true})
	if err != nil || r.status != http.StatusOK {
		return "", false
	}
	var out struct {
		Fingerprint string `json:"fingerprint"`
	}
	_ = json.Unmarshal(r.body, &out)
	return out.Fingerprint, true
}

func (w *mixWorker) sendBulk(ctx context.Context, op string, body map[string]any) {
	sendBulk(ctx, w.c, op, body)
}

func sendBulk(ctx context.Context, c *client, op string, body map[string]any) {
	r, err := c.do(ctx, op, http.MethodPost, "/api/documents/bulk", body)
	if err != nil || r.status != http.StatusOK {
		return
	}
	var out struct {
		Updated int `json:"updated"`
	}
	if json.Unmarshal(r.body, &out) == nil {
		c.st.bulkUpdated(op, out.Updated)
	}
}

// patch sends a single document's PATCH of its tags, correspondent or type.
func (w *mixWorker) patch(ctx context.Context) {
	v := w.m.view()
	doc := w.pick(w.c.user.docIDs)
	body := map[string]any{}
	switch w.rng.IntN(3) {
	case 0:
		ids := []string{}
		for range w.rng.IntN(4) {
			if t := w.pick(v.tags); t != "" && !slices.Contains(ids, t) {
				ids = append(ids, t)
			}
		}
		body["tagIds"] = ids
	case 1:
		if c := w.pick(v.corrs); c != "" && w.rng.IntN(2) == 0 {
			body["correspondentId"] = c
		} else {
			body["correspondentId"] = nil
		}
	default:
		if t := w.pick(v.types); t != "" && w.rng.IntN(2) == 0 {
			body["documentTypeId"] = t
		} else {
			body["documentTypeId"] = nil
		}
	}
	_, _ = w.c.do(ctx, "patch", http.MethodPatch, "/api/documents/"+doc, body)
}

// metadata replenishes the user's metadata or deletes, renames or moves
// some of it.
func (w *mixWorker) metadata(ctx context.Context) {
	v := w.m.view()
	if w.rng.IntN(2) == 0 && w.replenish(ctx, v) {
		return
	}
	switch r := w.rng.IntN(100); {
	case r < 20:
		w.deletePair(ctx, v)
	case r < 35:
		if t := w.pick(v.tags); t != "" {
			w.deleteTag(ctx, "tag_delete", t)
		}
	case r < 45:
		w.deleteNamed(ctx, "correspondent", w.pick(v.corrs))
	case r < 55:
		w.deleteNamed(ctx, "document_type", w.pick(v.types))
	case r < 80:
		w.rename(ctx, v)
	default:
		w.move(ctx, v)
	}
}

// replenish creates what is missing of review B's metadata; false when
// nothing is missing.
func (w *mixWorker) replenish(ctx context.Context, v view) bool {
	switch {
	case len(v.roots) < wantRoots:
		createTag(ctx, w.c, w.m, "tag_create", nil)
		return true
	case len(v.roots) > maxRoots:
		w.deleteTag(ctx, "tag_delete", w.pick(v.roots))
		return true
	}
	for _, root := range v.roots[:wantRoots] {
		if len(v.children[root]) < wantChildren {
			createTag(ctx, w.c, w.m, "tag_create", &root)
			return true
		}
	}
	switch {
	case len(v.corrs) < wantNamed:
		createNamed(ctx, w.c, w.m, "correspondent_create", "correspondent")
	case len(v.types) < wantNamed:
		createNamed(ctx, w.c, w.m, "document_type_create", "document_type")
	default:
		return false
	}
	return true
}

// deletePair deletes a top-level tag and one of its sub-tags at the same
// time.
func (w *mixWorker) deletePair(ctx context.Context, v view) {
	var withChildren []string
	for _, r := range v.roots {
		if len(v.children[r]) > 0 {
			withChildren = append(withChildren, r)
		}
	}
	parent := w.pick(withChildren)
	if parent == "" {
		return
	}
	child := w.pick(v.children[parent])
	var wg sync.WaitGroup
	wg.Go(func() { w.deleteTag(ctx, "tag_delete_parent", parent) })
	wg.Go(func() { w.deleteTag(ctx, "tag_delete_child", child) })
	wg.Wait()
}

func (w *mixWorker) deleteTag(ctx context.Context, op, id string) {
	r, err := w.c.do(ctx, op, http.MethodDelete, "/api/tags/"+id, nil)
	if err == nil && (r.status == http.StatusNoContent || r.status == http.StatusNotFound) {
		w.m.dropTag(id)
	}
}

func (w *mixWorker) deleteNamed(ctx context.Context, kind, id string) {
	if id == "" {
		return
	}
	r, err := w.c.do(ctx, kind+"_delete", http.MethodDelete, namedPath(kind)+"/"+id, nil)
	if err == nil && (r.status == http.StatusNoContent || r.status == http.StatusNotFound) {
		w.m.setNamed(kind, id, false)
	}
}

func (w *mixWorker) rename(ctx context.Context, v view) {
	switch w.rng.IntN(3) {
	case 0:
		if t := w.pick(v.tags); t != "" {
			_, _ = w.c.do(ctx, "tag_rename", http.MethodPatch, "/api/tags/"+t, map[string]any{"name": newName("tag")})
		}
	case 1:
		if c := w.pick(v.corrs); c != "" {
			_, _ = w.c.do(ctx, "correspondent_rename", http.MethodPatch, "/api/correspondents/"+c, map[string]any{"name": newName("corr")})
		}
	default:
		if t := w.pick(v.types); t != "" {
			_, _ = w.c.do(ctx, "document_type_rename", http.MethodPatch, "/api/document-types/"+t, map[string]any{"name": newName("type")})
		}
	}
}

// move moves a sub-tag under another top-level tag (mostly), makes it
// top-level, or tries to put a top-level tag under its own sub-tag (409
// tag_cycle, which exercises the cycle check's lock).
func (w *mixWorker) move(ctx context.Context, v view) {
	var subs []string
	for _, c := range v.children {
		subs = append(subs, c...)
	}
	slices.Sort(subs)
	var id string
	var parent any
	switch r := w.rng.IntN(10); {
	case r < 8:
		id = w.pick(subs)
		if id == "" {
			return
		}
		var others []string
		for _, root := range v.roots {
			if p := v.parent[id]; p == nil || *p != root {
				others = append(others, root)
			}
		}
		if parent = w.pick(others); parent == "" {
			return
		}
	case r < 9:
		if id = w.pick(subs); id == "" {
			return
		}
	default:
		var withChildren []string
		for _, root := range v.roots {
			if len(v.children[root]) > 0 {
				withChildren = append(withChildren, root)
			}
		}
		if id = w.pick(withChildren); id == "" {
			return
		}
		parent = w.pick(v.children[id])
	}
	r, err := w.c.do(ctx, "tag_move", http.MethodPatch, "/api/tags/"+id, map[string]any{"parentId": parent})
	if err == nil && r.status == http.StatusOK {
		var t struct {
			ParentID *string `json:"parentId"`
		}
		if json.Unmarshal(r.body, &t) == nil {
			w.m.setTag(id, t.ParentID)
		}
	}
}

func namedPath(kind string) string {
	if kind == "document_type" {
		return "/api/document-types"
	}
	return "/api/correspondents"
}

// createTag creates a tag (under parent, if given) and returns its ID, or
// "" when refused.
func createTag(ctx context.Context, c *client, m *metaCache, op string, parent *string) string {
	body := map[string]any{"name": newName("tag")}
	if parent != nil {
		body["parentId"] = *parent
	}
	r, err := c.do(ctx, op, http.MethodPost, "/api/tags", body)
	if err != nil || r.status != http.StatusCreated {
		return ""
	}
	var t struct {
		ID       string  `json:"id"`
		ParentID *string `json:"parentId"`
	}
	if json.Unmarshal(r.body, &t) != nil {
		return ""
	}
	m.setTag(t.ID, t.ParentID)
	return t.ID
}

func createNamed(ctx context.Context, c *client, m *metaCache, op, kind string) {
	r, err := c.do(ctx, op, http.MethodPost, namedPath(kind), map[string]any{"name": newName(kind)})
	if err != nil || r.status != http.StatusCreated {
		return
	}
	var x struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(r.body, &x) == nil {
		m.setNamed(kind, x.ID, true)
	}
}

// setupMetadata creates review B's metadata for the client's user through
// the API (not recorded).
func setupMetadata(ctx context.Context, c *client, m *metaCache) error {
	for range wantRoots {
		root := createTag(ctx, c, m, "", nil)
		if root == "" {
			return fmt.Errorf("create a tag for %s", c.user.Name)
		}
		for range wantChildren {
			if createTag(ctx, c, m, "", &root) == "" {
				return fmt.Errorf("create a sub-tag for %s", c.user.Name)
			}
		}
	}
	for range wantNamed {
		createNamed(ctx, c, m, "", "correspondent")
		createNamed(ctx, c, m, "", "document_type")
	}
	return m.refresh(ctx, c)
}

// runMix runs review B's mix: o.Threads threads for o.Duration, thread i
// as userOf(i). mark runs after the setup, right before the load. It
// returns how long the threads ran (until the last one finished its last
// request).
func runMix(ctx context.Context, e *env, o *options, users []*testUser, userOf func(int) *testUser, st *stats, mark func() error) (time.Duration, error) {
	caches := map[string]*metaCache{}
	for _, u := range users {
		c, err := login(ctx, e.baseURL, u, st)
		if err != nil {
			return 0, err
		}
		m := newMetaCache()
		if err := setupMetadata(ctx, c, m); err != nil {
			return 0, err
		}
		caches[u.Name] = m
		// The refresher keeps the cache near the server's state.
		refreshCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-refreshCtx.Done():
					return
				case <-t.C:
					_ = m.refresh(refreshCtx, c)
				}
			}
		}()
	}
	workers := make([]*mixWorker, o.Threads)
	for i := range workers {
		u := userOf(i)
		c, err := login(ctx, e.baseURL, u, st)
		if err != nil {
			return 0, err
		}
		//nolint:gosec // a seeded, reproducible mix, not a secret
		workers[i] = &mixWorker{c: c, m: caches[u.Name], rng: rand.New(rand.NewPCG(uint64(o.Seed), uint64(i))), flow: o.BulkFlow}
	}
	if err := mark(); err != nil {
		return 0, err
	}
	start := time.Now()
	deadline := start.Add(o.Duration)
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Go(func() {
			for time.Now().Before(deadline) && ctx.Err() == nil {
				w.step(ctx)
			}
		})
	}
	wg.Wait()
	return time.Since(start), ctx.Err()
}
