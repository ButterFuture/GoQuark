package download

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ButterFuture/GoQuark/internal/client"
)

// Options for multi-part download.
type Options struct {
	PartSize    int
	Concurrency int
	Limit       int64 // 0 = full file
	Resume      bool  // keep existing file + skip completed parts
	OnProgress  func(done, total int64, speed float64)
}

// Small-file threshold: below this, use single GET (no Range multipart).
// Official client: partition when size >= 8 MiB; we also use single stream for tiny CDN objects.
const smallFileMax = 4 << 20 // 4 MiB

// File downloads url into dest using official-like multipart strategy.
func File(ctx context.Context, c *client.Client, dlURL, dest string, opt Options) error {
	dlURL = strings.TrimSpace(dlURL)
	if dlURL == "" {
		return fmt.Errorf("empty download url")
	}
	cfg := c.Config()
	if opt.PartSize <= 0 {
		opt.PartSize = cfg.PartSize
		if opt.PartSize <= 0 {
			opt.PartSize = 4 << 20 // official default
		}
	}
	if opt.Concurrency <= 0 {
		opt.Concurrency = cfg.Concurrency
		if opt.Concurrency <= 0 {
			opt.Concurrency = 12 // VIP fallback taskMaxParallelNum
		}
	}

	cookie := cfg.CookieHeader()
	hc := c.DownloadHTTP()

	total, supportsRange, err := probeSize(ctx, hc, dlURL, cookie)
	if err != nil {
		return downloadSingle(ctx, hc, dlURL, cookie, dest, 0, opt)
	}
	target := total
	if opt.Limit > 0 && opt.Limit < total {
		target = opt.Limit
	}

	// Official SUPPORT_PARTITION at >= 8 MiB; keep single stream for smaller.
	if target > 0 && target <= smallFileMax {
		return downloadSingle(ctx, hc, dlURL, cookie, dest, target, opt)
	}
	if !supportsRange || target <= 0 {
		return downloadSingle(ctx, hc, dlURL, cookie, dest, target, opt)
	}

	return downloadMultipart(ctx, hc, dlURL, cookie, dest, target, opt)
}

func downloadSingle(ctx context.Context, hc *http.Client, dlURL, cookie, dest string, expected int64, opt Options) error {
	if opt.Resume && expected > 0 {
		if fi, err := os.Stat(dest); err == nil && fi.Size() == expected {
			if opt.OnProgress != nil {
				opt.OnProgress(expected, expected, 0)
			}
			return nil
		}
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := singleAttempt(ctx, hc, dlURL, cookie, dest, expected, opt)
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 300 * time.Millisecond):
		}
	}
	return lastErr
}

func singleAttempt(ctx context.Context, hc *http.Client, dlURL, cookie, dest string, expected int64, opt Options) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return err
	}
	setCDN(req, cookie)
	var resumeFrom int64
	if opt.Resume && expected > smallFileMax {
		if fi, e := os.Stat(dest); e == nil && fi.Size() > 0 && fi.Size() < expected {
			resumeFrom = fi.Size()
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
		}
	}

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	flags := os.O_CREATE | os.O_RDWR
	if resumeFrom == 0 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(dest, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if resumeFrom > 0 {
		if _, err := f.Seek(resumeFrom, io.SeekStart); err != nil {
			return err
		}
	}

	total := expected
	if total <= 0 {
		if cl := resp.ContentLength; cl > 0 {
			if resp.StatusCode == 206 && resumeFrom > 0 {
				total = resumeFrom + cl
			} else {
				total = cl
			}
		}
	}

	var done atomic.Int64
	done.Store(resumeFrom)
	prog := newSpeedReporter(opt.OnProgress, max64(total, 1), &done)
	defer prog.Stop()

	buf := make([]byte, 256<<10)
	start := time.Now()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done.Add(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("read body: %w", rerr)
		}
	}

	got := done.Load()
	if total > 0 && got != total {
		if got == 0 {
			return fmt.Errorf("empty body")
		}
		if expected > 0 && got < expected {
			return fmt.Errorf("incomplete: %d/%d in %s", got, expected, time.Since(start).Round(time.Millisecond))
		}
	}
	if opt.OnProgress != nil {
		opt.OnProgress(got, max64(total, got), 0)
	}
	_ = os.Remove(partMapPath(dest))
	return nil
}

func downloadMultipart(ctx context.Context, hc *http.Client, dlURL, cookie, dest string, target int64, opt Options) error {
	flags := os.O_CREATE | os.O_RDWR
	if !opt.Resume {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(dest, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(target); err != nil {
		return err
	}

	pmap := loadPartMap(dest, opt.PartSize, target)
	if !opt.Resume {
		pmap = newPartMap(opt.PartSize, target)
		_ = savePartMap(dest, pmap)
	}

	// Official-like connection warm: keep TLS pool hot before partition workers start.
	// unet uses disableSocketGroupLimits + connection reuse; warm reduces first-part gap.
	warmN := min(opt.Concurrency, 8)
	if target > 64<<20 {
		warmN = min(opt.Concurrency, 12)
	}
	warm(ctx, hc, dlURL, cookie, warmN)

	type job struct{ start, end int64 }
	// Buffered job queue: fixed workers pull continuously → no idle gap between parts.
	// Queue depth > concurrency so a finishing worker always has the next part ready.
	jobs := make(chan job, opt.Concurrency*4)
	go func() {
		defer close(jobs)
		for off := int64(0); off < target; {
			end := off + int64(opt.PartSize) - 1
			if end >= target {
				end = target - 1
			}
			if !pmap.done(off) {
				select {
				case <-ctx.Done():
					return
				case jobs <- job{off, end}:
				}
			}
			off = end + 1
		}
	}()

	var done atomic.Int64
	done.Store(pmap.completedBytes())
	prog := newSpeedReporter(opt.OnProgress, target, &done)
	defer prog.Stop()

	// Async partmap flush: workers only mark memory; disk write is debounced.
	// Syncing .gqparts on every part was a major inter-thread idle source.
	var pmu sync.Mutex
	dirty := false
	flushStop := make(chan struct{})
	var flushWG sync.WaitGroup
	flushWG.Add(1)
	go func() {
		defer flushWG.Done()
		t := time.NewTicker(400 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-flushStop:
				pmu.Lock()
				if dirty {
					_ = savePartMap(dest, pmap)
					dirty = false
				}
				pmu.Unlock()
				return
			case <-t.C:
				pmu.Lock()
				if dirty {
					_ = savePartMap(dest, pmap)
					dirty = false
				}
				pmu.Unlock()
			}
		}
	}()

	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	workers := opt.Concurrency
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					return
				}
				// Official opt path: fully buffer part on network, release conn to pool,
				// then WriteAt — minimizes connection idle time between parts.
				onByte := func(n int64) { done.Add(n) }
				n, err := fetchPartBuffered(ctx, hc, dlURL, cookie, f, j.start, j.end, onByte)
				if err != nil && ctx.Err() == nil {
					done.Add(-n)
					n, err = fetchPartBuffered(ctx, hc, dlURL, cookie, f, j.start, j.end, onByte)
				}
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					select {
					case errCh <- err:
					default:
					}
					return
				}
				pmu.Lock()
				pmap.mark(j.start)
				dirty = true
				pmu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Bytes are in (or almost in); tell UI we are finalizing BEFORE
	// waiting on partmap flush / file close — those can stall for seconds
	// on shared disks (vmhgfs) after a multi-GB WriteAt burst.
	if opt.OnProgress != nil {
		// force done=total so manager flips to StatusFinalizing
		done.Store(target)
		opt.OnProgress(target, target, 0)
	}

	close(flushStop)
	// don't block forever on flush; give it a short window then continue
	doneFlush := make(chan struct{})
	go func() {
		flushWG.Wait()
		close(doneFlush)
	}()
	select {
	case <-doneFlush:
	case <-time.After(2 * time.Second):
		// abandon waiting; final save may still finish in background
	}

	select {
	case err := <-errCh:
		return err
	default:
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	got := done.Load()
	if got != target {
		got = pmap.completedBytes()
		if got != target {
			return fmt.Errorf("incomplete: %d/%d", got, target)
		}
	}
	// partmap cleanup is best-effort; manager also removes async
	go func() { _ = os.Remove(partMapPath(dest)) }()
	// f.Close() via defer may still take time (page cache writeback) —
	// UI already shows finalizing spinner until File() returns.
	return nil
}

// --- speed reporter: sliding window + dual EMA (display-stable) ---

type speedSample struct {
	at    time.Time
	bytes int64
}

type speedReporter struct {
	stop chan struct{}
	wg   sync.WaitGroup
}

func newSpeedReporter(cb func(done, total int64, speed float64), total int64, done *atomic.Int64) *speedReporter {
	r := &speedReporter{stop: make(chan struct{})}
	if cb == nil {
		return r
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		// Sample often; report a blend of:
		//  1) sliding-window average over ~2.5s (main, IDM-like smoothness)
		//  2) dual EMA (fast + slow) as secondary damping
		// This kills the 4/8/16/32 stair-step look from part-aligned bursts.
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()

		const window = 2500 * time.Millisecond
		const maxSamples = 40 // ~4s at 100ms
		samples := make([]speedSample, 0, maxSamples)
		var emaFast, emaSlow float64
		const aFast, aSlow = 0.28, 0.08
		var lastShown float64
		const maxStep = 0.18 // max relative change per sample (rate-limit UI jumps)

		push := func(now time.Time, b int64) {
			samples = append(samples, speedSample{at: now, bytes: b})
			// drop old
			cut := 0
			for cut < len(samples) && now.Sub(samples[cut].at) > window {
				cut++
			}
			if cut > 0 {
				samples = samples[cut:]
			}
			if len(samples) > maxSamples {
				samples = samples[len(samples)-maxSamples:]
			}
		}

		windowRate := func() float64 {
			if len(samples) < 2 {
				return 0
			}
			first, last := samples[0], samples[len(samples)-1]
			dt := last.at.Sub(first.at).Seconds()
			if dt < 0.15 {
				return 0
			}
			db := float64(last.bytes - first.bytes)
			if db < 0 {
				return 0
			}
			return db / dt
		}

		now := time.Now()
		b0 := done.Load()
		push(now, b0)
		cb(b0, total, 0)

		for {
			select {
			case <-r.stop:
				return
			case now := <-t.C:
				b := done.Load()
				push(now, b)
				win := windowRate()

				// dual EMA on window rate (more stable than pure inst)
				if win > 0 || emaFast > 0 {
					if emaFast <= 0 {
						emaFast, emaSlow = win, win
					} else {
						emaFast = aFast*win + (1-aFast)*emaFast
						emaSlow = aSlow*win + (1-aSlow)*emaSlow
					}
				}
				// blend: mostly slow EMA, some window for responsiveness
				shown := 0.65*emaSlow + 0.25*emaFast + 0.10*win
				if shown < 0 {
					shown = 0
				}
				// rate-limit displayed jumps so UI doesn't thrash
				if lastShown > 1 {
					hi := lastShown * (1 + maxStep)
					lo := lastShown * (1 - maxStep)
					if shown > hi {
						shown = hi
					} else if shown < lo && shown > 0 {
						shown = lo
					}
				}
				// if truly idle for whole window, decay toward 0
				if win == 0 && len(samples) >= 2 {
					first, last := samples[0], samples[len(samples)-1]
					if last.bytes == first.bytes && last.at.Sub(first.at) >= window {
						shown *= 0.5
						if shown < 1024 {
							shown = 0
						}
						emaFast *= 0.5
						emaSlow *= 0.5
					}
				}
				lastShown = shown
				cb(b, total, shown)
			}
		}
	}()
	return r
}

func (r *speedReporter) Stop() {
	if r == nil || r.stop == nil {
		return
	}
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	r.wg.Wait()
}

// --- part map ---

type partMap struct {
	PartSize int             `json:"part_size"`
	Total    int64           `json:"total"`
	Done     map[string]bool `json:"done"`
}

func newPartMap(partSize int, total int64) *partMap {
	return &partMap{PartSize: partSize, Total: total, Done: map[string]bool{}}
}

func partMapPath(dest string) string {
	return dest + ".gqparts"
}

func loadPartMap(dest string, partSize int, total int64) *partMap {
	b, err := os.ReadFile(partMapPath(dest))
	if err != nil {
		return newPartMap(partSize, total)
	}
	var p partMap
	if json.Unmarshal(b, &p) != nil || p.Done == nil || p.Total != total || p.PartSize != partSize {
		return newPartMap(partSize, total)
	}
	return &p
}

func savePartMap(dest string, p *partMap) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(partMapPath(dest), b, 0o600)
}

func (p *partMap) key(start int64) string {
	return fmt.Sprintf("%d", start)
}

func (p *partMap) done(start int64) bool {
	return p.Done[p.key(start)]
}

func (p *partMap) mark(start int64) {
	p.Done[p.key(start)] = true
}

func (p *partMap) completedBytes() int64 {
	var n int64
	for off := int64(0); off < p.Total; {
		end := off + int64(p.PartSize) - 1
		if end >= p.Total {
			end = p.Total - 1
		}
		if p.done(off) {
			n += end - off + 1
		}
		off = end + 1
	}
	return n
}

func warm(ctx context.Context, hc *http.Client, dlURL, cookie string, n int) {
	if n <= 0 {
		return
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
			setCDN(req, cookie)
			req.Header.Set("Range", "bytes=0-0")
			resp, err := hc.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8))
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
}

// setCDN: official CDN GET is lean — Cookie + Cache-Control (+ Range by caller).
// Heavy Origin/Referer/UA on CDN was measured slower than lean headers.
func setCDN(req *http.Request, cookie string) {
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	// Official renderer multipart: Cookie + Cache-Control (+ Range by caller).
	// Keep a minimal Accept so some CDNs don't 406; avoid Origin/Referer/heavy UA.
	req.Header.Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Connection", "keep-alive")
}

func probeSize(ctx context.Context, hc *http.Client, dlURL, cookie string) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return 0, false, err
	}
	setCDN(req, cookie)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("probe: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64))
	resp.Body.Close()

	if resp.StatusCode == 206 {
		if n, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			return n, true, nil
		}
	}
	if resp.StatusCode == 200 && resp.ContentLength > 0 {
		return resp.ContentLength, false, nil
	}

	req2, err := http.NewRequestWithContext(ctx, http.MethodHead, dlURL, nil)
	if err == nil {
		setCDN(req2, cookie)
		if resp2, err2 := hc.Do(req2); err2 == nil {
			cl := resp2.ContentLength
			resp2.Body.Close()
			if cl > 0 {
				return cl, false, nil
			}
		}
	}

	req3, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return 0, false, err
	}
	setCDN(req3, cookie)
	resp3, err := hc.Do(req3)
	if err != nil {
		return 0, false, fmt.Errorf("probe get: %w", err)
	}
	cl := resp3.ContentLength
	_, _ = io.Copy(io.Discard, io.LimitReader(resp3.Body, 1))
	resp3.Body.Close()
	if cl > 0 {
		return cl, false, nil
	}
	return 0, false, fmt.Errorf("cannot determine size (status %d)", resp.StatusCode)
}

func parseContentRangeTotal(cr string) (int64, bool) {
	if cr == "" {
		return 0, false
	}
	i := strings.LastIndex(cr, "/")
	if i < 0 || i+1 >= len(cr) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(cr[i+1:]), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// fetchPartBuffered matches the opt demo / official-like path:
//   1) ReadFull entire Range part into memory (conn returns to idle pool ASAP)
//   2) single WriteAt to disk
// Progress is reported as bytes arrive from the network so UI speed is continuous.
func fetchPartBuffered(ctx context.Context, hc *http.Client, dlURL, cookie string, f *os.File, start, end int64, onByte func(int64)) (int64, error) {
	want := end - start + 1
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return 0, err
	}
	setCDN(req, cookie)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("part %d-%d: %w", start, end, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return 0, fmt.Errorf("part %d-%d status %d %s", start, end, resp.StatusCode, string(b))
	}

	buf := make([]byte, want)
	var got int64
	// chunked ReadFull so we can report progress mid-part
	for got < want {
		if ctx.Err() != nil {
			return got, ctx.Err()
		}
		chunk := 256 << 10
		if rem := int(want - got); rem < chunk {
			chunk = rem
		}
		n, rerr := resp.Body.Read(buf[got : got+int64(chunk)])
		if n > 0 {
			got += int64(n)
			if onByte != nil {
				onByte(int64(n))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return got, fmt.Errorf("part %d-%d read: %w", start, end, rerr)
		}
	}
	if got != want {
		return got, fmt.Errorf("part %d-%d short read %d/%d", start, end, got, want)
	}
	if _, err := f.WriteAt(buf, start); err != nil {
		return got, err
	}
	return got, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
