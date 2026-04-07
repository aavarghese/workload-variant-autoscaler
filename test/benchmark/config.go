package benchmark

import (
	"github.com/llm-d/llm-d-workload-variant-autoscaler/test/testconfig"
)

// BenchmarkConfig holds configuration for benchmark tests loaded from environment variables.
// Common fields are inherited from testconfig.SharedConfig.
type BenchmarkConfig struct {
	testconfig.SharedConfig

	// Gateway configuration (benchmark routes through the full llm-d stack)
	GatewayServiceName string
	GatewayServicePort int

	// Benchmark-specific
	BenchmarkResultsFile string

	// Grafana
	GrafanaEnabled          bool   // Deploy ephemeral Grafana and capture snapshot
	GrafanaSnapshotFile     string // Path to write snapshot URL
	GrafanaSnapshotJSONFile string // Path to export full snapshot JSON (re-importable)
	GrafanaPanelDir         string // Directory to write rendered panel PNGs

	// Phase durations (seconds, overridable via env for tuning)
	BaselineDurationSec  int
	SpikeDurationSec     int
	SustainedDurationSec int
	CooldownDurationSec  int

	// FMA (Fast Model Actuation) configuration
	FMAEnabled              bool   // Enable FMA actuation benchmark scenario
	FMANamespace            string // Namespace where FMA objects are created (defaults to LLMDNamespace)
	FMAModelID              string // Model for InferenceServerConfig
	FMAModelPort            int32  // Port for the vLLM server inside launcher
	FMAMaxSleepingInstances int32  // Max sleeping instances in LauncherConfig
	FMALauncherImage        string // Launcher pod container image
	FMARequesterImage       string // Requester pod container image
	FMAIterations           int    // Number of scale 0->1->0 iterations
	FMAWarmupDurationSec    int    // Warmup phase: let launchers populate
	FMAIterationTimeoutSec  int    // Timeout per actuation iteration
	FMACooldownDurationSec  int    // Cooldown between iterations
	FMAResultsFile          string // Path to write FMA benchmark results JSON
}

// LoadConfigFromEnv reads benchmark configuration from environment variables.
func LoadConfigFromEnv() BenchmarkConfig {
	shared := testconfig.LoadSharedConfig()

	gatewayServiceDefault := "infra-inference-scheduling-inference-gateway-istio"
	if shared.Environment == "kind-emulator" {
		gatewayServiceDefault = "infra-sim-inference-gateway-istio"
	}

	return BenchmarkConfig{
		SharedConfig: shared,

		GatewayServiceName: testconfig.GetEnv("GATEWAY_SERVICE_NAME", gatewayServiceDefault),
		GatewayServicePort: testconfig.GetEnvInt("GATEWAY_SERVICE_PORT", 80),

		BenchmarkResultsFile: testconfig.GetEnv("BENCHMARK_RESULTS_FILE", "/tmp/benchmark-results.json"),

		GrafanaEnabled:          testconfig.GetEnvBool("BENCHMARK_GRAFANA_ENABLED", true),
		GrafanaSnapshotFile:     testconfig.GetEnv("BENCHMARK_GRAFANA_SNAPSHOT_FILE", "/tmp/benchmark-grafana-snapshot.txt"),
		GrafanaSnapshotJSONFile: testconfig.GetEnv("BENCHMARK_GRAFANA_SNAPSHOT_JSON", "/tmp/benchmark-grafana-snapshot.json"),
		GrafanaPanelDir:         testconfig.GetEnv("BENCHMARK_GRAFANA_PANEL_DIR", "/tmp/benchmark-panels"),

		BaselineDurationSec:  testconfig.GetEnvInt("BENCHMARK_BASELINE_DURATION", 120),
		SpikeDurationSec:     testconfig.GetEnvInt("BENCHMARK_SPIKE_DURATION", 300),
		SustainedDurationSec: testconfig.GetEnvInt("BENCHMARK_SUSTAINED_DURATION", 180),
		CooldownDurationSec:  testconfig.GetEnvInt("BENCHMARK_COOLDOWN_DURATION", 300),

		FMAEnabled:              testconfig.GetEnvBool("FMA_ENABLED", true),
		FMANamespace:            testconfig.GetEnv("FMA_NAMESPACE", shared.LLMDNamespace),
		FMAModelID:              testconfig.GetEnv("FMA_MODEL_ID", "HuggingFaceTB/SmolLM2-360M-Instruct"),
		FMAModelPort:            int32(testconfig.GetEnvInt("FMA_MODEL_PORT", 8005)),
		FMAMaxSleepingInstances: int32(testconfig.GetEnvInt("FMA_MAX_SLEEPING_INSTANCES", 1)),
		FMALauncherImage:        testconfig.GetEnv("FMA_LAUNCHER_IMAGE", ""),
		FMARequesterImage:       testconfig.GetEnv("FMA_REQUESTER_IMAGE", ""),
		FMAIterations:           testconfig.GetEnvInt("FMA_ITERATIONS", 5),
		FMAWarmupDurationSec:    testconfig.GetEnvInt("FMA_WARMUP_DURATION", 60),
		FMAIterationTimeoutSec:  testconfig.GetEnvInt("FMA_ITERATION_TIMEOUT", 120),
		FMACooldownDurationSec:  testconfig.GetEnvInt("FMA_COOLDOWN_DURATION", 30),
		FMAResultsFile:          testconfig.GetEnv("FMA_BENCHMARK_RESULTS_FILE", "/tmp/fma-benchmark-results.json"),
	}
}
