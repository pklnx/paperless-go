//go:build loadtest

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// uploadPDF is a unique file of about size bytes that the server accepts
// as a PDF (its type is detected from the header; processing is off).
func uploadPDF(tag string, size int) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%%PDF-1.4\n%% paperless-go load test %s\n", tag)
	pad := make([]byte, 48)
	for b.Len() < size-16 {
		_, _ = rand.Read(pad)
		fmt.Fprintf(&b, "%% %x\n", pad)
	}
	b.WriteString("%%EOF\n")
	return b.Bytes()
}

// uploadLoop uploads o.Uploads files one after the other as c's user, one
// every o.UploadInterval (or right after the previous one when that took
// longer), so the uploads sample the whole bulk loop rather than finishing
// inside its first change.
func uploadLoop(ctx context.Context, c *client, o *options, op string) {
	t := time.NewTicker(o.UploadInterval)
	defer t.Stop()
	for i := range o.Uploads {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
		name := fmt.Sprintf("%s-%d.pdf", op, i)
		_, _ = c.upload(ctx, op, name, uploadPDF(name, o.UploadSize))
	}
}

// runUploads is S3: first a baseline (alice and bob upload at the same
// time, nothing else runs), then the same uploads while alice's bulk
// changes by match over all her seeded documents run in a loop.
func runUploads(ctx context.Context, e *env, o *options, alice, bob *testUser, st *stats, mark func() error) error {
	ca, err := login(ctx, e.baseURL, alice, st)
	if err != nil {
		return err
	}
	cb, err := login(ctx, e.baseURL, bob, st)
	if err != nil {
		return err
	}
	tag := createTag(ctx, ca, newMetaCache(), "", nil)
	if tag == "" {
		return fmt.Errorf("create the bulk loop's tag")
	}
	bulk, err := login(ctx, e.baseURL, alice, st)
	if err != nil {
		return err
	}
	if err := mark(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Go(func() { uploadLoop(ctx, ca, o, "upload_baseline_alice") })
	wg.Go(func() { uploadLoop(ctx, cb, o, "upload_baseline_bob") })
	wg.Wait()

	// The seeded documents are out of the inbox and uploads go into it, so
	// the match stays exactly alice's seeded documents (MaxBulkMatches).
	match := map[string]any{"inbox": false}
	changes := []map[string]any{
		{"favorite": true}, {"addTags": []string{tag}}, {"favorite": false}, {"removeTags": []string{tag}},
	}
	var done atomic.Bool
	started := make(chan struct{})
	var startOnce sync.Once
	var loop sync.WaitGroup
	loop.Go(func() {
		defer startOnce.Do(func() { close(started) })
		for i := 0; !done.Load() && ctx.Err() == nil; i++ {
			body := map[string]any{"match": match}
			for k, v := range changes[i%len(changes)] {
				body[k] = v
			}
			if o.BulkFlow == flowFingerprint {
				if fp, ok := dryRun(ctx, bulk, match); ok && fp != "" {
					body["fingerprint"] = fp
				}
			}
			startOnce.Do(func() { close(started) })
			sendBulk(ctx, bulk, "bulk_loop", body)
		}
	})
	<-started
	wg.Go(func() { uploadLoop(ctx, ca, o, "upload_bulk_alice") })
	wg.Go(func() { uploadLoop(ctx, cb, o, "upload_bulk_bob") })
	wg.Wait()
	done.Store(true)
	loop.Wait()
	return ctx.Err()
}
