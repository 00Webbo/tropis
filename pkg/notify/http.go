package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// poster sends JSON with a timeout and a few retries on transient failures.
type poster struct {
	client  *http.Client
	retries int
	backoff time.Duration
}

func newPoster() poster {
	return poster{client: &http.Client{Timeout: 10 * time.Second}, retries: 3, backoff: time.Second}
}

func (p poster) post(ctx context.Context, url string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var last error
	for attempt := 0; attempt < p.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(p.backoff * time.Duration(attempt)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "tropis-notify")
		resp, err := p.client.Do(req)
		if err != nil {
			last = err
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		switch {
		case resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			last = fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
			continue
		default:
			// A 4xx other than 429 will not get better by retrying.
			return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
		}
	}
	return last
}

// Webhook posts a change as JSON to a URL, for anything that is not Slack.
//
// The payload is the Change itself, with a type field, carrying the full
// previous and current verdicts — the same documents NodeHealthReports hold
// as .status.
type Webhook struct {
	URL string
	p   poster
}

// NewWebhook returns a generic webhook notifier.
func NewWebhook(url string) *Webhook { return &Webhook{URL: url, p: newPoster()} }

// Name identifies the notifier.
func (w *Webhook) Name() string { return "webhook" }

// Notify posts the change.
func (w *Webhook) Notify(ctx context.Context, c Change) error {
	return w.p.post(ctx, w.URL, struct {
		Type    string `json:"type"`
		Summary string `json:"summary"`
		Change
	}{Type: "tropis.verdict.changed", Summary: Headline(c), Change: c})
}
