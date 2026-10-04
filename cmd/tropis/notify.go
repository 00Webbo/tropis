package main

import (
	"errors"
	"os"
	"strconv"

	"k8s.io/client-go/kubernetes"

	"github.com/00Webbo/tropis/pkg/k8s"
	"github.com/00Webbo/tropis/pkg/notify"
)

// notifyConfig is read from the environment only. Webhook URLs are
// credentials — anyone holding a Slack incoming-webhook URL can post to the
// channel — and flags would expose them in pod specs and process listings.
// The chart sets these from Secrets.
//
//	TROPIS_SLACK_WEBHOOK_URL  Slack incoming webhook
//	TROPIS_WEBHOOK_URL        generic JSON webhook
//	TROPIS_EVENTS             "true" to record Kubernetes Events on Nodes
//	TROPIS_NOTIFY_ON          causal (default) or any
type notifyConfig struct {
	slackURL   string
	webhookURL string
	events     bool
	policy     notify.Policy
}

func notifyConfigFromEnv() (notifyConfig, error) {
	c := notifyConfig{
		slackURL:   os.Getenv("TROPIS_SLACK_WEBHOOK_URL"),
		webhookURL: os.Getenv("TROPIS_WEBHOOK_URL"),
	}
	var err error
	if c.policy, err = notify.ParsePolicy(os.Getenv("TROPIS_NOTIFY_ON")); err != nil {
		return c, err
	}
	if s := os.Getenv("TROPIS_EVENTS"); s != "" {
		if c.events, err = strconv.ParseBool(s); err != nil {
			return c, errors.New("TROPIS_EVENTS must be true or false")
		}
	}
	return c, nil
}

// notifiers builds the configured set, or nil if none is configured.
func (c notifyConfig) notifiers(cs kubernetes.Interface) notify.Notifier {
	var m notify.Multi
	if c.slackURL != "" {
		m = append(m, notify.NewSlack(c.slackURL))
	}
	if c.webhookURL != "" {
		m = append(m, notify.NewWebhook(c.webhookURL))
	}
	if c.events {
		host, _ := os.Hostname()
		m = append(m, &k8s.EventNotifier{Client: cs, Instance: host})
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
