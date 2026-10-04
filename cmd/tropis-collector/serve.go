package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/00Webbo/tropis/pkg/host/smart"
	"github.com/00Webbo/tropis/pkg/schema"
)

// SMARTPath is the endpoint serving the node's host capture.
const SMARTPath = "/v1/smart"

// captureCache rate-limits SMART collection and keeps a short history.
//
// SMART queries are not free — on a degrading drive they can take seconds and
// contend with real I/O — so a burst of requests must not become a burst of
// queries.
//
// The history exists because a single snapshot cannot tell a drive that has
// carried the same defect count for a year from one that gained it this
// morning. Snapshots are kept at most one per historyInterval, for up to
// historyMax, and the oldest is served as the capture's Previous.
type captureCache struct {
	mu              sync.Mutex
	collect         func(ctx context.Context) (map[string][]byte, error)
	minInterval     time.Duration
	historyInterval time.Duration
	historyMax      time.Duration
	now             func() time.Time

	last    *schema.HostCapture
	history []schema.HostSnapshot
}

func (c *captureCache) get(ctx context.Context) (*schema.HostCapture, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now().UTC()
	if c.last != nil && now.Sub(c.last.CollectedAt) < c.minInterval {
		return c.last, nil
	}

	raw, err := c.collect(ctx)
	if err != nil {
		return nil, err
	}
	snap := schema.HostSnapshot{SMART: make(map[string]schema.RawJSON, len(raw)), CollectedAt: now}
	for dev, data := range raw {
		snap.SMART[dev] = schema.RawJSON(data)
	}
	c.record(snap)

	capture := &schema.HostCapture{SMART: snap.SMART, CollectedAt: now}
	if oldest := c.history[0]; oldest.CollectedAt.Before(now) {
		prev := oldest
		capture.Previous = &prev
	}
	c.last = capture
	return capture, nil
}

// record adds a snapshot to the history if enough time has passed since the
// newest one, and drops snapshots older than historyMax. The newest snapshot
// is always kept, so there is a baseline from the first collection onward.
func (c *captureCache) record(snap schema.HostSnapshot) {
	interval, max := c.historyInterval, c.historyMax
	if interval <= 0 {
		interval = time.Hour
	}
	if max <= 0 {
		max = 24 * time.Hour
	}
	if n := len(c.history); n == 0 || snap.CollectedAt.Sub(c.history[n-1].CollectedAt) >= interval {
		c.history = append(c.history, snap)
	}
	cutoff := snap.CollectedAt.Add(-max)
	for len(c.history) > 1 && c.history[0].CollectedAt.Before(cutoff) {
		c.history = c.history[1:]
	}
}

// refreshLoop collects on a fixed cadence, so history accumulates whether or
// not anyone is asking for it.
func (c *captureCache) refreshLoop(ctx context.Context, every time.Duration, logw io.Writer) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := c.get(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(logw, "refresh: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *captureCache) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(SMARTPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		capture, err := c.get(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(capture)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

func runServe(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var src sourceFlags
	src.register(fs)
	addr := fs.String("addr", ":9476", "listen address")
	minInterval := fs.Duration("min-interval", time.Minute, "minimum time between SMART collections")
	historyInterval := fs.Duration("history-interval", time.Hour, "how often to collect in the background and record a history snapshot")
	historyMax := fs.Duration("history-max", 24*time.Hour, "how far back history is kept")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	devices := fs.Args()

	collect := func(ctx context.Context) (map[string][]byte, error) {
		if src.fromDir != "" {
			return rawFromDir(src.fromDir)
		}
		c := &smart.Collector{Binary: src.smartctl, Timeout: src.timeout}
		return c.CollectRaw(ctx, devices)
	}

	cache := &captureCache{
		collect:         collect,
		minInterval:     *minInterval,
		historyInterval: *historyInterval,
		historyMax:      *historyMax,
		now:             time.Now,
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           cache.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		// Generous: a cold request may wait on SMART from every device.
		WriteTimeout: 5 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	go cache.refreshLoop(ctx, *historyInterval, stderr)

	fmt.Fprintf(stderr, "tropis-collector serving %s on %s\n", SMARTPath, *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(stderr, "serve: %v\n", err)
		return exitError
	}
	return exitOK
}

// rawFromDir reads saved smartctl output as a raw capture keyed by device,
// using the same file-name convention as reportFromDir.
func rawFromDir(dir string) (map[string][]byte, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(matches))
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		out["/dev/"+strings.TrimSuffix(filepath.Base(m), ".json")] = data
	}
	return out, nil
}
