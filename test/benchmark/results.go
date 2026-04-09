package benchmark

import (
	"encoding/json"
	"math"
	"os"
)

// BenchmarkResults holds the collected metrics from a benchmark scenario.
// This struct is shared across all benchmark scenarios.
type BenchmarkResults struct {
	ScaleUpTimeSec     float64 `json:"scaleUpTimeSec"`
	ScaleDownTimeSec   float64 `json:"scaleDownTimeSec"`
	MaxReplicas        int32   `json:"maxReplicas"`
	AvgKVCacheUsage    float64 `json:"avgKVCacheUsage"`
	AvgQueueDepth      float64 `json:"avgQueueDepth"`
	ReplicaOscillation float64 `json:"replicaOscillation"`
	TotalDurationSec   float64 `json:"totalDurationSec"`
	GrafanaSnapshotURL string  `json:"grafanaSnapshotUrl,omitempty"`
}

// stddev computes the standard deviation of a float64 slice.
func stddev(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))

	var variance float64
	for _, v := range values {
		diff := v - mean
		variance += diff * diff
	}
	variance /= float64(len(values))

	return math.Sqrt(variance)
}

// FMABenchmarkResults holds metrics from an FMA actuation benchmark.
type FMABenchmarkResults struct {
	ColdActuationTimesMs []float64 `json:"coldActuationTimesMs"`
	WarmActuationTimesMs []float64 `json:"warmActuationTimesMs"`
	AvgColdActuationMs   float64   `json:"avgColdActuationMs"`
	AvgWarmActuationMs   float64   `json:"avgWarmActuationMs"`
	HitRate              float64   `json:"hitRate"`
	TotalIterations      int       `json:"totalIterations"`
	WarmHits             int       `json:"warmHits"`
	ColdStarts           int       `json:"coldStarts"`
	TTFTTimesMs          []float64 `json:"ttftTimesMs,omitempty"`
	AvgTTFTMs            float64   `json:"avgTTFTMs,omitempty"`
	TotalDurationSec     float64   `json:"totalDurationSec"`
}

// Finalize computes derived fields (averages, hit rate) from raw iteration data.
func (r *FMABenchmarkResults) Finalize() {
	r.TotalIterations = len(r.ColdActuationTimesMs) + len(r.WarmActuationTimesMs)
	r.ColdStarts = len(r.ColdActuationTimesMs)
	r.WarmHits = len(r.WarmActuationTimesMs)
	if r.TotalIterations > 0 {
		r.HitRate = float64(r.WarmHits) / float64(r.TotalIterations)
	}
	r.AvgColdActuationMs = mean(r.ColdActuationTimesMs)
	r.AvgWarmActuationMs = mean(r.WarmActuationTimesMs)
	r.AvgTTFTMs = mean(r.TTFTTimesMs)
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}


// writeFMAResults writes FMA benchmark results to a JSON file.
func writeFMAResults(results *FMABenchmarkResults, path string) error {
	results.Finalize()
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
