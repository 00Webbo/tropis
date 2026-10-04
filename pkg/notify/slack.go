package notify

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Slack posts a change to a Slack incoming webhook.
type Slack struct {
	URL string
	p   poster
}

// NewSlack returns a Slack notifier for an incoming-webhook URL.
func NewSlack(url string) *Slack { return &Slack{URL: url, p: newPoster()} }

// Name identifies the notifier.
func (s *Slack) Name() string { return "slack" }

// Notify posts the change.
func (s *Slack) Notify(ctx context.Context, c Change) error {
	return s.p.post(ctx, s.URL, SlackMessage(c))
}

// escape neutralises Slack's control characters, so text taken from a
// verdict cannot form links, mentions or channel pings.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// SlackMessage renders a change as a Block Kit message.
func SlackMessage(c Change) map[string]any {
	v := c.Current
	icon := map[Kind]string{KindNew: ":rotating_light:", KindChanged: ":rotating_light:", KindCleared: ":white_check_mark:"}[c.Kind]
	if v.Relationship != "causal" && c.Kind != KindCleared {
		icon = ":mag:"
	}
	headline := Headline(c)

	var body strings.Builder
	if c.Kind == KindCleared && c.Previous != nil {
		fmt.Fprintf(&body, "Previously *%s*", c.Previous.Relationship)
		if c.Previous.RootCause != nil {
			fmt.Fprintf(&body, " (%s): %s", c.Previous.RootCause.Layer, escape(c.Previous.RootCause.Description))
		}
		body.WriteString("\n")
	}
	if v.RootCause != nil {
		fmt.Fprintf(&body, "*Root cause* (%s layer): %s\n", v.RootCause.Layer, escape(v.RootCause.Description))
	}
	if len(v.Evidence) > 0 {
		body.WriteString("*Evidence*\n")
		for i, e := range v.Evidence {
			if i == 4 {
				fmt.Fprintf(&body, "• …and %d more\n", len(v.Evidence)-4)
				break
			}
			fmt.Fprintf(&body, "• `%s` %s\n", escape(e.Ref), escape(truncate(e.Excerpt, 200)))
		}
	}
	if v.NextStep != "" {
		fmt.Fprintf(&body, "*Next step* (advisory): %s\n", escape(v.NextStep))
	}

	return map[string]any{
		// Notification and accessibility fallback.
		"text": icon + " Tropis: " + escape(headline),
		"blocks": []any{
			map[string]any{"type": "header", "text": map[string]any{
				"type": "plain_text", "text": truncate("Tropis: "+headline, 150), "emoji": true,
			}},
			map[string]any{"type": "section", "text": map[string]any{
				"type": "mrkdwn", "text": truncate(body.String(), 2900),
			}},
			map[string]any{"type": "context", "elements": []any{map[string]any{
				"type": "mrkdwn",
				"text": fmt.Sprintf("%s Tropis has taken no action. %s/%s, prompt %s, observed %s",
					icon, escape(v.Backend.Provider), escape(v.Backend.Model), escape(v.Backend.PromptVersion),
					v.ObservedAt.UTC().Format(time.RFC3339)),
			}}},
		},
	}
}
