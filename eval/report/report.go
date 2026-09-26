// Package report renders evaluation results for people.
//
// The machine-readable results.json is the record; this Markdown is a view
// of it. Everything here is derived from runner.Results, so the two cannot
// disagree.
package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nathanwebb/tropis/eval/runner"
	"github.com/nathanwebb/tropis/pkg/schema"
)

var relationships = []string{
	string(schema.RelationshipCausal),
	string(schema.RelationshipCoincidental),
	string(schema.RelationshipInsufficientEvidence),
}

// Markdown renders a report.
func Markdown(r *runner.Results) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# Tropis evaluation report\n\n")
	if r.Synthetic {
		w("> **DEVELOPMENT CORPUS — NOT PUBLISHABLE.** Every fixture in this run is\n")
		w("> synthetic. The numbers below exercise the harness and say nothing about\n")
		w("> Tropis's diagnostic accuracy. Do not publish or quote them.\n\n")
	}

	w("| | |\n|---|---|\n")
	w("| Run | %s |\n", r.StartedAt.Format("2006-01-02 15:04 MST"))
	w("| Backend | %s / %s |\n", r.Backend.Provider, r.Backend.Model)
	w("| Prompt | %s |\n", r.Backend.PromptVersion)
	w("| Agent | %s |\n", r.AgentVersion)
	w("| Corpus | `%s` (%d fixtures) |\n", r.Corpus, len(r.Cases))
	w("| Seed | %d |\n", r.Seed)
	if r.Rig != nil {
		w("| Rig | %s, Kubernetes %s, %d nodes |\n", r.Rig.Cluster, r.Rig.KubernetesVersion, r.Rig.Nodes)
	}
	w("\n")

	w("## Gate\n\n")
	w("**%s** — %s\n\n", strings.ToUpper(strings.ReplaceAll(r.Gate.Status, "_", " ")), r.Gate.Reason)
	w("The bar, fixed before any result: correct root cause on ≥%.0f%% of at least %d injected faults, with a false-correlation rate under %.0f%% on negative controls.\n\n",
		100*runner.GateMinRootCauseAccuracy, runner.GateMinPositives, 100*runner.GateMaxFalseCorrelationRate)

	if len(r.Warnings) > 0 {
		w("## Warnings\n\n")
		for _, warn := range r.Warnings {
			w("- %s\n", warn)
		}
		w("\n")
	}

	w("## Headline\n\n")
	cols := []string{"All"}
	metrics := []runner.Metrics{r.Metrics}
	for _, v := range []string{string(schema.VariantNPDAbsent), string(schema.VariantNPDPresent)} {
		if m, ok := r.ByVariant[v]; ok {
			cols = append(cols, v)
			metrics = append(metrics, m)
		}
	}
	w("| Metric | %s |\n|---|%s\n", strings.Join(cols, " | "), strings.Repeat("---|", len(cols)))
	row := func(name string, f func(runner.Metrics) string) {
		vals := make([]string, len(metrics))
		for i, m := range metrics {
			vals[i] = f(m)
		}
		w("| %s | %s |\n", name, strings.Join(vals, " | "))
	}
	row("Root-cause accuracy (positives)", func(m runner.Metrics) string {
		return fmt.Sprintf("%s (%d/%d)", pct(m.RootCauseAccuracy), m.RootCauseCorrect, m.Positives)
	})
	row("False-correlation rate (negative controls)", func(m runner.Metrics) string {
		return fmt.Sprintf("%s (%d/%d)", pct(m.FalseCorrelationRate), m.FalseCorrelations, m.NegativeControls)
	})
	row("Causal claims on any non-causal scenario", func(m runner.Metrics) string {
		return fmt.Sprintf("%d/%d", m.CausalClaimsOnNonCausal, m.NonCausal)
	})
	row("Relationship accuracy (all)", func(m runner.Metrics) string { return pct(m.RelationshipAccuracy) })
	row("Pre-filter recall (positives raised)", func(m runner.Metrics) string { return pct(m.PrefilterRecall) })
	row("Brier score (lower is better)", func(m runner.Metrics) string { return fmt.Sprintf("%.3f", m.Calibration.Brier) })
	row("Expected calibration error", func(m runner.Metrics) string { return fmt.Sprintf("%.3f", m.Calibration.ECE) })
	w("\nNPD is optional enrichment: if the two variant columns differ materially, accuracy depends on NPD being installed.\n\n")

	w("## Confusion\n\nRows are the true relationship; columns are the verdict.\n\n")
	predCols := append(append([]string{}, relationships...), "error")
	w("| True \\ Verdict | %s |\n|---|%s\n", strings.Join(predCols, " | "), strings.Repeat("---|", len(predCols)))
	for _, exp := range relationships {
		vals := make([]string, len(predCols))
		for i, p := range predCols {
			vals[i] = fmt.Sprint(r.Metrics.Confusion[exp][p])
		}
		w("| %s | %s |\n", exp, strings.Join(vals, " | "))
	}
	w("\n")

	w("## Calibration\n\n| Confidence | Verdicts | Mean confidence | Accuracy |\n|---|---|---|---|\n")
	for _, bin := range r.Metrics.Calibration.Bins {
		if bin.Count == 0 {
			continue
		}
		w("| %.1f–%.1f | %d | %.2f | %s |\n", bin.Lower, bin.Upper, bin.Count, bin.MeanConfidence, pct(bin.Accuracy))
	}
	w("\n")

	w("## Cases\n\n| Scenario | Variant | Expected | Verdict | Confidence | Correct | Pre-filter |\n|---|---|---|---|---|---|---|\n")
	cases := append([]runner.CaseResult(nil), r.Cases...)
	sort.Slice(cases, func(i, j int) bool {
		if cases[i].ScenarioID != cases[j].ScenarioID {
			return cases[i].ScenarioID < cases[j].ScenarioID
		}
		return cases[i].Variant < cases[j].Variant
	})
	for _, c := range cases {
		got, conf := "error: "+c.ErrorKind, "—"
		if c.Verdict != nil {
			got = describe(c.Verdict.Relationship, layerOf(c.Verdict))
			conf = fmt.Sprintf("%.2f", c.Verdict.Confidence)
		}
		raised := "—"
		if c.Raised {
			raised = strings.Join(c.TriggeredBy, ", ")
		}
		w("| %s | %s | %s | %s | %s | %s | %s |\n",
			c.ScenarioID, c.Variant, describe(c.Expected.Relationship, c.Expected.RootCauseLayer), got, conf, tick(c.Score.Correct), raised)
	}
	return b.String()
}

func layerOf(v *schema.Verdict) *schema.Layer {
	if v.RootCause == nil {
		return nil
	}
	l := v.RootCause.Layer
	return &l
}

func describe(rel schema.Relationship, l *schema.Layer) string {
	if l != nil && rel == schema.RelationshipCausal {
		return fmt.Sprintf("%s (%s)", rel, *l)
	}
	return string(rel)
}

func pct(f float64) string { return fmt.Sprintf("%.1f%%", 100*f) }

func tick(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
