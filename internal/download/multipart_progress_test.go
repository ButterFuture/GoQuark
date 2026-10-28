package download

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rangeServer serves size bytes with Range support, slowly enough that a
// download can be stopped halfway. failFrom >= 0 makes parts starting at or
// after that offset answer 500.
func rangeServer(t *testing.T, size int64, failFrom int64) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int64 = 0, size - 1
		if rg := strings.TrimPrefix(r.Header.Get("Range"), "bytes="); rg != "" {
			a, b, _ := strings.Cut(rg, "-")
			start, _ = strconv.ParseInt(a, 10, 64)
			if b != "" {
				end, _ = strconv.ParseInt(b, 10, 64)
			}
		}
		if failFrom >= 0 && start >= failFrom && end > start {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if end > start {
			time.Sleep(30 * time.Millisecond)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(make([]byte, end-start+1))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A multipart download stopped by pause/cancel or a failed part must not
// report 100%: the manager takes done >= total as finished and would mark the
// task done (or, mid-pause, cancelled) with most of the file missing.
func TestMultipartStoppedNeverReports100(t *testing.T) {
	const size = 64 << 16 // 64 parts of 64 KiB
	for _, tc := range []struct {
		name     string
		failFrom int64
		stopAt   time.Duration
		wantErr  func(error) bool
	}{
		{"paused", -1, 120 * time.Millisecond, func(err error) bool { return errors.Is(err, context.Canceled) }},
		{"part fails", size / 2, 0, func(err error) bool { return err != nil && strings.Contains(err.Error(), "status 500") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := rangeServer(t, size, tc.failFrom)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.stopAt > 0 {
				time.AfterFunc(tc.stopAt, cancel)
			}
			var last atomic.Int64
			err := downloadMultipart(ctx, srv.Client(), srv.URL, "", filepath.Join(t.TempDir(), "f.bin"), size, Options{
				PartSize: 64 << 10, Concurrency: 2,
				OnProgress: func(done, total int64, _ float64) { last.Store(done) },
			})
			if !tc.wantErr(err) {
				t.Fatalf("err = %v", err)
			}
			if got := last.Load(); got >= size {
				t.Errorf("last progress %d of %d: a stopped download reported 100%%", got, size)
			}
		})
	}

	srv := rangeServer(t, size, -1)
	var last atomic.Int64
	if err := downloadMultipart(context.Background(), srv.Client(), srv.URL, "", filepath.Join(t.TempDir(), "f.bin"), size, Options{
		PartSize: 64 << 10, Concurrency: 8,
		OnProgress: func(done, total int64, _ float64) { last.Store(done) },
	}); err != nil {
		t.Fatal(err)
	}
	if last.Load() != size {
		t.Errorf("complete download last progress %d, want %d", last.Load(), size)
	}
}
