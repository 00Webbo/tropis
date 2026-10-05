package runner

import (
	"fmt"
	"math"

	"github.com/00Webbo/tropis/pkg/schema"
)

// Gate thresholds, fixed in advance of any result. "Diagnostic accuracy is
// proven" otherwise means whatever is convenient on the day.
//
// The gate was first set on root-cause accuracy alone. Before any real
// result existed, it was moved to end-to-end detection (proposal 0001, D2):
// reasoning accuracy alone would let a pre-filter that never fires pass,
// while an operator only ever sees faults that were raised and diagnosed.
// The false-correlation bar is unchanged.
const (
	GateMinPositives            = 20
	GateMinEndToEndDetection    = 0.80
	GateMaxFalseCorrelationRate = 0.10
	calibrationBins             = 10
	predictedError              = "error"
)

// Metrics summarise a set of case results.
type Metrics struct {
	Cases int `json:"cases"`

	// EndToEndDetection is the fraction of positives the pre-filter raised
	// and the reasoning layer then diagnosed correctly: what an operator of
	// a live cluster experiences, and the number the gate applies to. Every
	// fixture is analysed whether or not it was raised, but a live sweep
	// analyses only what is raised, so this is at most both PrefilterRecall
	// and RootCauseAccuracy.
	EndToEndCorrect   int     `json:"endToEndCorrect"`
	EndToEndDetection float64 `json:"endToEndDetection"`

	// Positives are causal scenarios. RootCauseAccuracy is the fraction whose
	// verdict names the right relationship and layer, whether or not the
	// pre-filter raised them; errors count as wrong.
	Positives         int     `json:"positives"`
	RootCauseCorrect  int     `json:"rootCauseCorrect"`
	RootCauseAccuracy float64 `json:"rootCauseAccuracy"`

	// NegativeControls are coincidental scenarios. FalseCorrelationRate is
	// the fraction answered causal.
	NegativeControls     int     `json:"negativeControls"`
	FalseCorrelations    int     `json:"falseCorrelations"`
	FalseCorrelationRate float64 `json:"falseCorrelationRate"`

	// CausalClaimsOnNonCausal widens the false-correlation count to include
	// insufficient-evidence scenarios.
	NonCausal               int `json:"nonCausal"`
	CausalClaimsOnNonCausal int `json:"causalClaimsOnNonCausal"`

	// RelationshipAccuracy is exact agreement on the three-way relationship.
	RelationshipAccuracy float64 `json:"relationshipAccuracy"`

	// Confusion counts expected (outer) against predicted (inner), with
	// "error" as a predicted value.
	Confusion map[string]map[string]int `json:"confusion"`

	// Predicted counts each answer given.
	Predicted map[string]int `json:"predicted"`

	// Errors by kind.
	Errors map[string]int `json:"errors,omitempty"`

	// Calibration compares stated confidence with actual correctness.
	Calibration Calibration `json:"calibration"`

	// PrefilterRecall is the fraction of positives the pre-filter raised.
	PrefilterRecall float64 `json:"prefilterRecall"`
	// PrefilterRaisedNonCausal counts non-causal scenarios it raised anyway.
	PrefilterRaisedNonCausal int `json:"prefilterRaisedNonCausal"`
}

// Calibration measures how well confidence tracks correctness.
type Calibration struct {
	// Bins partition verdicts by stated confidence.
	Bins []CalibrationBin `json:"bins"`
	// Brier is the mean squared error between confidence and correctness
	// (0 is perfect; always answering 0.5 scores 0.25).
	Brier float64 `json:"brier"`
	// ECE is the expected calibration error: the verdict-weighted mean gap
	// between confidence and accuracy across bins.
	ECE float64 `json:"ece"`
}

// CalibrationBin is one confidence band.
type CalibrationBin struct {
	Lower          float64 `json:"lower"`
	Upper          float64 `json:"upper"`
	Count          int     `json:"count"`
	MeanConfidence float64 `json:"meanConfidence"`
	Accuracy       float64 `json:"accuracy"`
}

// Gate is the pre-registered bar for starting remediation work.
type Gate struct {
	Status string `json:"status"` // pass, fail, not_applicable
	Reason string `json:"reason"`
}

func computeMetrics(cases []CaseResult) Metrics {
	m := Metrics{
		Cases:     len(cases),
		Confusion: map[string]map[string]int{},
		Predicted: map[string]int{},
		Errors:    map[string]int{},
	}
	var relCorrect, raisedPositives int
	type point struct {
		conf    float64
		correct bool
	}
	var points []point

	for _, c := range cases {
		exp := string(c.Expected.Relationship)
		pred := predictedError
		if c.Verdict != nil {
			pred = string(c.Verdict.Relationship)
			points = append(points, point{c.Verdict.Confidence, c.Score.Correct})
		} else {
			m.Errors[c.ErrorKind]++
		}
		if m.Confusion[exp] == nil {
			m.Confusion[exp] = map[string]int{}
		}
		m.Confusion[exp][pred]++
		m.Predicted[pred]++
		if c.Score.RelationshipCorrect {
			relCorrect++
		}

		switch c.Expected.Relationship {
		case schema.RelationshipCausal:
			m.Positives++
			if c.Score.Correct {
				m.RootCauseCorrect++
			}
			if c.Raised {
				raisedPositives++
				if c.Score.Correct {
					m.EndToEndCorrect++
				}
			}
		case schema.RelationshipCoincidental:
			m.NegativeControls++
			if c.Score.FalseCorrelation {
				m.FalseCorrelations++
			}
		}
		if c.Expected.Relationship != schema.RelationshipCausal {
			m.NonCausal++
			if c.Score.FalseCorrelation {
				m.CausalClaimsOnNonCausal++
			}
			if c.Raised {
				m.PrefilterRaisedNonCausal++
			}
		}
	}
	if len(m.Errors) == 0 {
		m.Errors = nil
	}

	m.EndToEndDetection = ratio(m.EndToEndCorrect, m.Positives)
	m.RootCauseAccuracy = ratio(m.RootCauseCorrect, m.Positives)
	m.FalseCorrelationRate = ratio(m.FalseCorrelations, m.NegativeControls)
	m.RelationshipAccuracy = ratio(relCorrect, m.Cases)
	m.PrefilterRecall = ratio(raisedPositives, m.Positives)

	// Calibration over verdicts only: an error states no confidence.
	bins := make([]CalibrationBin, calibrationBins)
	sums := make([]float64, calibrationBins)
	hits := make([]int, calibrationBins)
	var brier float64
	for i := range bins {
		bins[i].Lower = float64(i) / calibrationBins
		bins[i].Upper = float64(i+1) / calibrationBins
	}
	for _, p := range points {
		i := int(p.conf * calibrationBins)
		if i >= calibrationBins {
			i = calibrationBins - 1
		}
		if i < 0 {
			i = 0
		}
		bins[i].Count++
		sums[i] += p.conf
		outcome := 0.0
		if p.correct {
			hits[i]++
			outcome = 1
		}
		brier += (p.conf - outcome) * (p.conf - outcome)
	}
	var ece float64
	for i := range bins {
		if bins[i].Count == 0 {
			continue
		}
		bins[i].MeanConfidence = round(sums[i] / float64(bins[i].Count))
		bins[i].Accuracy = round(float64(hits[i]) / float64(bins[i].Count))
		ece += float64(bins[i].Count) / float64(len(points)) * math.Abs(bins[i].MeanConfidence-bins[i].Accuracy)
	}
	m.Calibration = Calibration{Bins: bins}
	if len(points) > 0 {
		m.Calibration.Brier = round(brier / float64(len(points)))
		m.Calibration.ECE = round(ece)
	}
	return m
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return round(float64(n) / float64(d))
}

func round(f float64) float64 { return math.Round(f*10000) / 10000 }

// evaluateGate applies the pre-registered bar. It never passes on a
// synthetic corpus, whatever the numbers say.
func evaluateGate(r *Results) Gate {
	m := r.Metrics
	switch {
	case r.Synthetic:
		return Gate{"not_applicable", "the corpus contains synthetic fixtures; the gate applies only to fixtures captured from real hardware"}
	case m.Positives < GateMinPositives:
		return Gate{"not_applicable", fmt.Sprintf("%d positive scenarios; the gate needs at least %d", m.Positives, GateMinPositives)}
	case m.NegativeControls == 0:
		return Gate{"not_applicable", "no negative controls; the false-correlation rate cannot be measured"}
	case m.EndToEndDetection >= GateMinEndToEndDetection && m.FalseCorrelationRate < GateMaxFalseCorrelationRate:
		return Gate{"pass", fmt.Sprintf("end-to-end detection %.1f%% ≥ %.0f%% and false-correlation rate %.1f%% < %.0f%%",
			100*m.EndToEndDetection, 100*GateMinEndToEndDetection, 100*m.FalseCorrelationRate, 100*GateMaxFalseCorrelationRate)}
	default:
		return Gate{"fail", fmt.Sprintf("end-to-end detection %.1f%% (needs ≥ %.0f%%), false-correlation rate %.1f%% (needs < %.0f%%)",
			100*m.EndToEndDetection, 100*GateMinEndToEndDetection, 100*m.FalseCorrelationRate, 100*GateMaxFalseCorrelationRate)}
	}
}

func warnings(r *Results) []string {
	var w []string
	m := r.Metrics
	if r.Synthetic {
		w = append(w, "DEVELOPMENT CORPUS: these fixtures are synthetic. The numbers below say nothing about Tropis and must not be published or quoted.")
	}
	// A reasoning layer that never declines to find causation is broken,
	// however well it scores on positives.
	if m.Predicted[string(schema.RelationshipCoincidental)] == 0 {
		w = append(w, "The backend never answered coincidental. A reasoning layer that never returns it is not working, regardless of accuracy.")
	}
	if m.Predicted[string(schema.RelationshipInsufficientEvidence)] == 0 {
		w = append(w, "The backend never answered insufficient_evidence. A reasoning layer that never returns it is not working, regardless of accuracy.")
	}
	if n := m.Predicted[predictedError]; n > 0 {
		w = append(w, fmt.Sprintf("%d of %d analyses produced no verdict (%v); they are scored as wrong.", n, m.Cases, m.Errors))
	}

	// Every scenario should be present in both NPD variants.
	variants := map[string]map[string]bool{}
	for _, c := range r.Cases {
		if variants[c.ScenarioID] == nil {
			variants[c.ScenarioID] = map[string]bool{}
		}
		variants[c.ScenarioID][c.Variant] = true
	}
	var missing int
	for _, v := range variants {
		if !v[string(schema.VariantNPDAbsent)] || !v[string(schema.VariantNPDPresent)] {
			missing++
		}
	}
	if missing > 0 {
		w = append(w, fmt.Sprintf("%d scenarios lack one of the two NPD variants; the NPD-dependence comparison is incomplete.", missing))
	}
	return w
}
