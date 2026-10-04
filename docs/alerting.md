# Notifications and alerting

Tropis sweeps every node on a schedule (every 15 minutes by default) and
writes each verdict as a `NodeHealthReport`. There are three ways to hear
about a verdict without running `kubectl get nhr`, and they combine.

| Channel | Best for | Needs |
|---|---|---|
| Slack or a generic webhook | A team channel, or wiring into anything that accepts JSON | A webhook URL in a Secret |
| Prometheus and Alertmanager | Teams that already route alerts there, with silencing and on-call | kube-state-metrics, Prometheus Operator for the bundled rule |
| Kubernetes Events on the Node | `kubectl describe node`, and any event exporter | Opt-in; a narrow extra permission |

## When something is sent

Slack, webhooks and Events fire on **changes**, never on every sweep. Only
the relationship and the root-cause layer are compared: the description,
confidence and evidence are written by the model and vary from run to run
on an unchanged node, and comparing them would notify every sweep.

With `notifications.on: causal` (the default), a notification is sent when:

- a node gets its first verdict and it is `causal`;
- a node becomes `causal`, or a causal node's root-cause layer changes;
- a causal node stops being causal ("cleared") — the fault has gone, or the
  evidence for it has.

`coincidental` and `insufficient_evidence` on their own are findings, not
calls to act, and stay quiet. With `notifications.on: any`, every change of
relationship or layer is sent, along with every node's first verdict.

A node whose report is `causal` is re-analysed on every sweep even if the
pre-filter no longer raises it, so a recovered node always reaches "cleared"
rather than staying causal forever.

## Slack

Create an [incoming webhook](https://api.slack.com/messaging/webhooks) for the
channel, store its URL in a Secret, and point the chart at it:

```sh
kubectl -n tropis-system create secret generic tropis-slack \
  --from-literal=url='https://hooks.slack.com/services/...'

helm upgrade tropis deploy/helm/tropis -n tropis-system --reuse-values \
  --set notifications.slack.webhookSecret.name=tropis-slack
```

The webhook URL is a credential — anyone holding it can post to the channel —
so the chart only ever reads it from a Secret.

Each message carries the headline, the root cause, up to four pieces of
cited evidence, the advisory next step, and the model and prompt version.
Text from the verdict is escaped, so it cannot form links or ping the
channel.

## Generic webhook

```sh
kubectl -n tropis-system create secret generic tropis-webhook \
  --from-literal=url='https://alerts.example.internal/tropis'

helm upgrade tropis deploy/helm/tropis -n tropis-system --reuse-values \
  --set notifications.webhook.urlSecret.name=tropis-webhook
```

Tropis POSTs JSON:

```json
{
  "type": "tropis.verdict.changed",
  "summary": "worker-03: causal, host layer (confidence 0.87)",
  "change": "new",
  "node": "worker-03",
  "previous": null,
  "verdict": { "...": "the same document a NodeHealthReport carries as .status" }
}
```

`change` is `new`, `changed` or `cleared`. Transient failures (5xx, 429) are
retried three times; a failed delivery makes the sweep exit non-zero, so the
CronJob shows it, without losing the verdict, which is already written.

## Prometheus and Alertmanager

kube-state-metrics can export `NodeHealthReport` resources as metrics, needing
only read access to them:

```sh
helm install ksm prometheus-community/kube-state-metrics -n monitoring \
  -f deploy/kube-state-metrics/values.yaml
```

That produces:

| Metric | Labels | Value |
|---|---|---|
| `tropis_nodehealthreport_confidence` | `node`, `relationship`, `layer` | confidence of the latest verdict |
| `tropis_nodehealthreport_observed_timestamp_seconds` | `node` | when its signals were collected |

With the Prometheus Operator, the chart can install the alert rules:

```sh
helm upgrade tropis deploy/helm/tropis -n tropis-system --reuse-values \
  --set alerting.prometheusRule.enabled=true \
  --set alerting.prometheusRule.labels.release=prometheus
```

- `TropisCausalVerdict` fires while a node's latest verdict is causal at or
  above `alerting.prometheusRule.minConfidence`, and resolves when it clears.
- `TropisReportStale` fires when a node has had no fresh verdict for three
  hours — a stopped sweep otherwise looks exactly like a healthy cluster.

Without the Operator, the same expressions work in a plain Prometheus rules
file; see `deploy/helm/tropis/templates/prometheusrule.yaml`.

Alertmanager then routes to Slack, PagerDuty, email or anything else it
supports, with grouping and silencing. If you already run it, this is the
route to prefer for paging; the Slack notifier suits a team channel.

## Kubernetes Events

```sh
helm upgrade tropis deploy/helm/tropis -n tropis-system --reuse-values \
  --set notifications.events.enabled=true
```

Each change is recorded as an Event on the Node — `TropisCausalVerdict`
(Warning), `TropisVerdictCleared` or `TropisVerdictChanged` (Normal) — visible
in `kubectl describe node` and to any event exporter.

This is **off by default** because it is the one write Tropis can make beyond
its own reports. Enabling it grants the analyser `create` on
`events.k8s.io/events` in the `default` namespace only, where Kubernetes keeps
events about Nodes. An Event changes nothing about a node or its workloads,
and expires on the API server's event TTL (an hour by default), so treat it
as a convenience, not a record: the `NodeHealthReport` is the record.

## Other tools

Tools that watch custom resources and forward to chat — Botkube, Robusta and
similar — can watch `nodehealthreports.tropis.io` directly, with no Tropis
configuration at all.
