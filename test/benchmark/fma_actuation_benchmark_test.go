package benchmark

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/test/e2e/fixtures"
)

const (
	envKindEmulator = "kind-emulator"
	labelValueTrue  = "true"
)

// FMAScenarioResources holds names of FMA objects created during benchmark setup.
type FMAScenarioResources struct {
	ISCName            string
	LauncherConfigName string
	LPPName            string
	DeploymentName     string
}

var _ = Describe("FMA Actuation Benchmark", Label("benchmark", "fma"), Ordered, func() {
	var (
		fmaRes        FMAScenarioResources
		fmaResults    FMABenchmarkResults
		scenarioStart time.Time
	)

	BeforeAll(func() {
		if !benchCfg.FMAEnabled {
			Skip("FMA benchmark disabled (FMA_ENABLED=false)")
		}
		Expect(benchCfg.FMALauncherImage).NotTo(BeEmpty(), "FMA_LAUNCHER_IMAGE is required")
		Expect(benchCfg.FMARequesterImage).NotTo(BeEmpty(), "FMA_REQUESTER_IMAGE is required")

		scenarioStart = time.Now()

		fmaRes = FMAScenarioResources{
			ISCName:            "bench-isc",
			LauncherConfigName: "bench-lc",
			LPPName:            "bench-lpp",
			DeploymentName:     "bench-requester",
		}

		ns := benchCfg.FMANamespace
		mockGPUs := benchCfg.Environment == envKindEmulator

		// Set up FMA prerequisites for Kind emulator:
		// - Label GPU nodes with nvidia.com/gpu.present=true (required by FMA's LPP)
		// - Create gpu-map ConfigMap with fake GPU mappings
		// - Create service accounts and RBAC for launcher and requester pods
		By("Labeling GPU nodes with nvidia.com/gpu.present=true")
		nodes, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		for _, node := range nodes.Items {
			if qty, ok := node.Status.Allocatable["nvidia.com/gpu"]; ok && !qty.IsZero() {
				if node.Labels["nvidia.com/gpu.present"] != labelValueTrue {
					node.Labels["nvidia.com/gpu.present"] = labelValueTrue
					_, updateErr := k8sClient.CoreV1().Nodes().Update(ctx, &node, metav1.UpdateOptions{})
					if updateErr != nil {
						GinkgoWriter.Printf("Warning: failed to label node %s: %v\n", node.Name, updateErr)
					} else {
						GinkgoWriter.Printf("Labeled node %s with nvidia.com/gpu.present=true\n", node.Name)
					}
				}
				// Create gpu-map for this node if running with mock GPUs
				if mockGPUs {
					gpuCount := int(qty.Value())
					By(fmt.Sprintf("Creating gpu-map ConfigMap for node %s (%d GPUs)", node.Name, gpuCount))
					gpuErr := fixtures.EnsureFMAGPUMap(ctx, k8sClient, ns, node.Name, gpuCount)
					Expect(gpuErr).NotTo(HaveOccurred(), "Failed to create gpu-map")
				}
			}
		}

		By("Setting up FMA RBAC (service accounts, roles, role bindings)")
		err = fixtures.EnsureFMARBAC(ctx, k8sClient, ns)
		Expect(err).NotTo(HaveOccurred(), "Failed to create FMA RBAC")

		By("Creating InferenceServerConfig")
		iscOpts := []fixtures.ISCOption{}
		if !mockGPUs {
			// Only enable sleep mode on real GPU platforms (not Kind emulator on ARM)
			iscOpts = append(iscOpts, fixtures.WithISCSleepMode())
		}
		err = fixtures.EnsureISC(ctx, crClient, ns, fmaRes.ISCName,
			benchCfg.FMAModelID, benchCfg.FMAModelPort, fmaRes.LauncherConfigName, iscOpts...)
		Expect(err).NotTo(HaveOccurred(), "Failed to create ISC")

		DeferCleanup(func() {
			_ = fixtures.DeleteISC(ctx, crClient, ns, fmaRes.ISCName)
		})

		By("Creating LauncherConfig")
		lcOpts := []fixtures.LCOption{}
		if mockGPUs {
			lcOpts = append(lcOpts, fixtures.WithLCImagePullPolicy(corev1.PullIfNotPresent))
		}
		err = fixtures.EnsureLC(ctx, crClient, ns, fmaRes.LauncherConfigName,
			benchCfg.FMAMaxSleepingInstances, benchCfg.FMALauncherImage, mockGPUs, lcOpts...)
		Expect(err).NotTo(HaveOccurred(), "Failed to create LauncherConfig")

		DeferCleanup(func() {
			_ = fixtures.DeleteLC(ctx, crClient, ns, fmaRes.LauncherConfigName)
		})

		By("Creating LauncherPopulationPolicy")
		err = fixtures.EnsureLPP(ctx, crClient, ns, fmaRes.LPPName, fmaRes.LauncherConfigName, 1)
		Expect(err).NotTo(HaveOccurred(), "Failed to create LPP")

		DeferCleanup(func() {
			_ = fixtures.DeleteLPP(ctx, crClient, ns, fmaRes.LPPName)
		})

		By("Creating requester Deployment at 0 replicas")
		deployOpts := []fixtures.FMADeploymentOption{}
		if mockGPUs {
			deployOpts = append(deployOpts, fixtures.WithFMAImagePullPolicy(corev1.PullIfNotPresent))
		}
		err = fixtures.CreateFMADeployment(ctx, k8sClient, ns, fmaRes.DeploymentName,
			fmaRes.ISCName, benchCfg.FMARequesterImage, 0, deployOpts...)
		Expect(err).NotTo(HaveOccurred(), "Failed to create requester Deployment")

		DeferCleanup(func() {
			_ = fixtures.DeleteFMADeployment(ctx, k8sClient, ns, fmaRes.DeploymentName)
		})
	})

	AfterAll(func() {
		if !benchCfg.FMAEnabled {
			return
		}
		fmaResults.TotalDurationSec = time.Since(scenarioStart).Seconds()

		By("Writing FMA benchmark results")
		err := writeFMAResults(&fmaResults, benchCfg.FMAResultsFile)
		Expect(err).NotTo(HaveOccurred(), "Failed to write FMA results")

		GinkgoWriter.Printf("FMA benchmark results written to %s\n", benchCfg.FMAResultsFile)
		GinkgoWriter.Printf("  Cold starts: %d, avg %.1f ms\n", fmaResults.ColdStarts, fmaResults.AvgColdActuationMs)
		GinkgoWriter.Printf("  Warm hits:   %d, avg %.1f ms\n", fmaResults.WarmHits, fmaResults.AvgWarmActuationMs)
		GinkgoWriter.Printf("  Hit rate:    %.2f\n", fmaResults.HitRate)
	})

	It("Phase 1: Warmup — verify launcher pods populate", func() {
		ns := benchCfg.FMANamespace
		warmupTimeout := time.Duration(benchCfg.FMAWarmupDurationSec) * time.Second

		By(fmt.Sprintf("Waiting up to %s for launcher pods to be Ready", warmupTimeout))
		err := fixtures.WaitForFMALaunchers(ctx, k8sClient, ns, fmaRes.LauncherConfigName, 1, warmupTimeout)
		Expect(err).NotTo(HaveOccurred(), "Launcher pods should become ready during warmup")

		pods, err := k8sClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "dual-pods.llm-d.ai/launcher-config-name=" + fmaRes.LauncherConfigName,
		})
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Warmup complete: %d launcher pod(s) ready\n", len(pods.Items))
	})

	It("Phase 2: Cold actuation iterations", func() {
		ns := benchCfg.FMANamespace
		iterTimeout := time.Duration(benchCfg.FMAIterationTimeoutSec) * time.Second
		cooldown := time.Duration(benchCfg.FMACooldownDurationSec) * time.Second
		halfIterations := benchCfg.FMAIterations / 2
		if halfIterations < 1 {
			halfIterations = 1
		}

		for i := 0; i < halfIterations; i++ {
			By(fmt.Sprintf("Cold iteration %d/%d: scale 0→1", i+1, halfIterations))
			iterStart := time.Now()

			err := fixtures.ScaleFMADeployment(ctx, k8sClient, ns, fmaRes.DeploymentName, 1)
			Expect(err).NotTo(HaveOccurred())

			err = fixtures.WaitForFMARequesterReady(ctx, k8sClient, ns, fmaRes.DeploymentName, 1, iterTimeout)
			Expect(err).NotTo(HaveOccurred(), "Requester should become ready")

			actuationMs := float64(time.Since(iterStart).Milliseconds())
			fmaResults.ColdActuationTimesMs = append(fmaResults.ColdActuationTimesMs, actuationMs)
			GinkgoWriter.Printf("  Cold iteration %d: %.0f ms\n", i+1, actuationMs)

			By(fmt.Sprintf("Cold iteration %d/%d: scale 1→0", i+1, halfIterations))
			err = fixtures.ScaleFMADeployment(ctx, k8sClient, ns, fmaRes.DeploymentName, 0)
			Expect(err).NotTo(HaveOccurred())

			err = fixtures.WaitForFMAScaleDown(ctx, k8sClient, ns, fmaRes.DeploymentName, iterTimeout)
			Expect(err).NotTo(HaveOccurred(), "RS should scale down to 0")

			if i < halfIterations-1 {
				time.Sleep(cooldown)
			}
		}
	})

	It("Phase 3: Warm actuation iterations (sleeping instances expected)", func() {
		ns := benchCfg.FMANamespace
		iterTimeout := time.Duration(benchCfg.FMAIterationTimeoutSec) * time.Second
		cooldown := time.Duration(benchCfg.FMACooldownDurationSec) * time.Second
		remainingIterations := benchCfg.FMAIterations - benchCfg.FMAIterations/2
		if remainingIterations < 1 {
			remainingIterations = 1
		}

		// Allow sleeping instances to settle after Phase 2
		time.Sleep(cooldown)

		for i := 0; i < remainingIterations; i++ {
			By(fmt.Sprintf("Warm iteration %d/%d: snapshot sleeping launchers", i+1, remainingIterations))

			// Before scale-up: record which launcher pods have sleeping instances
			sleepingLaunchers := map[string]bool{}
			launcherPods, err := k8sClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
				LabelSelector: "dual-pods.llm-d.ai/launcher-config-name=" + fmaRes.LauncherConfigName,
			})
			Expect(err).NotTo(HaveOccurred())
			for _, pod := range launcherPods.Items {
				if pod.Labels["dual-pods.llm-d.ai/sleeping"] == labelValueTrue {
					sleepingLaunchers[pod.Name] = true
				}
			}
			GinkgoWriter.Printf("  Sleeping launchers before scale-up: %d\n", len(sleepingLaunchers))

			By(fmt.Sprintf("Warm iteration %d/%d: scale 0→1", i+1, remainingIterations))
			iterStart := time.Now()

			err = fixtures.ScaleFMADeployment(ctx, k8sClient, ns, fmaRes.DeploymentName, 1)
			Expect(err).NotTo(HaveOccurred())

			err = fixtures.WaitForFMARequesterReady(ctx, k8sClient, ns, fmaRes.DeploymentName, 1, iterTimeout)
			Expect(err).NotTo(HaveOccurred(), "Requester should become ready")

			actuationMs := float64(time.Since(iterStart).Milliseconds())

			// After scale-up: check which launcher the requester was bound to
			// by reading the dual-pods.llm-d.ai/dual label on the requester pod
			requesterPods, err := k8sClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
				LabelSelector: "app=fma-benchmark,instance=" + fmaRes.DeploymentName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(requesterPods.Items).NotTo(BeEmpty(), "Requester pod should exist")

			boundLauncher := requesterPods.Items[0].Labels["dual-pods.llm-d.ai/dual"]
			instanceID := requesterPods.Items[0].Labels["dual-pods.llm-d.ai/instance"]

			// Classify: if the bound launcher was sleeping before scale-up, it's a hot start (wake-up)
			if sleepingLaunchers[boundLauncher] {
				fmaResults.WarmActuationTimesMs = append(fmaResults.WarmActuationTimesMs, actuationMs)
				GinkgoWriter.Printf("  Warm iteration %d: %.0f ms (HOT — woke sleeping instance %s on %s)\n", i+1, actuationMs, instanceID, boundLauncher)
			} else {
				fmaResults.ColdActuationTimesMs = append(fmaResults.ColdActuationTimesMs, actuationMs)
				GinkgoWriter.Printf("  Warm iteration %d: %.0f ms (MISS — new instance %s on %s)\n", i+1, actuationMs, instanceID, boundLauncher)
			}

			By(fmt.Sprintf("Warm iteration %d/%d: scale 1→0", i+1, remainingIterations))
			err = fixtures.ScaleFMADeployment(ctx, k8sClient, ns, fmaRes.DeploymentName, 0)
			Expect(err).NotTo(HaveOccurred())

			err = fixtures.WaitForFMAScaleDown(ctx, k8sClient, ns, fmaRes.DeploymentName, iterTimeout)
			Expect(err).NotTo(HaveOccurred(), "RS should scale down to 0")

			if i < remainingIterations-1 {
				time.Sleep(cooldown)
			}
		}
	})

	It("FMA Load Benchmark: GuideLLM with VA+HPA against FMA launcher", func() {
		ns := benchCfg.FMANamespace
		iterTimeout := time.Duration(benchCfg.FMAIterationTimeoutSec) * time.Second

		By("Ensuring FMA Deployment has 1 ready replica")
		err := fixtures.ScaleFMADeployment(ctx, k8sClient, ns, fmaRes.DeploymentName, 1)
		Expect(err).NotTo(HaveOccurred())
		err = fixtures.WaitForFMARequesterReady(ctx, k8sClient, ns, fmaRes.DeploymentName, 1, iterTimeout)
		Expect(err).NotTo(HaveOccurred(), "FMA Deployment should have 1 ready replica")

		By("Creating VariantAutoscaling for FMA Deployment")
		err = fixtures.EnsureVariantAutoscalingWithDefaults(
			ctx, crClient, ns, "fma-va",
			fmaRes.DeploymentName, benchCfg.FMAModelID, benchCfg.AcceleratorType,
			benchCfg.ControllerInstance,
		)
		Expect(err).NotTo(HaveOccurred(), "Failed to create VA for FMA")
		DeferCleanup(func() {
			_ = fixtures.DeleteVariantAutoscaling(ctx, crClient, ns, "fma-va")
		})

		By("Creating HPA for FMA Deployment")
		err = fixtures.EnsureHPA(ctx, k8sClient, ns, "fma-hpa", fmaRes.DeploymentName, "fma-va", 1, 10)
		Expect(err).NotTo(HaveOccurred(), "Failed to create HPA for FMA")
		DeferCleanup(func() {
			_ = fixtures.DeleteHPA(ctx, k8sClient, ns, "fma-hpa")
		})

		By("Discovering launcher vLLM endpoint")
		requesterPods, err := k8sClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "app=fma-benchmark,instance=" + fmaRes.DeploymentName,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(requesterPods.Items).NotTo(BeEmpty(), "Requester pod should exist")

		launcherName := requesterPods.Items[0].Labels["dual-pods.llm-d.ai/dual"]
		instanceID := requesterPods.Items[0].Labels["dual-pods.llm-d.ai/instance"]
		Expect(launcherName).NotTo(BeEmpty(), "Requester should be bound to a launcher")

		launcherPod, err := k8sClient.CoreV1().Pods(ns).Get(ctx, launcherName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		launcherIP := launcherPod.Status.PodIP
		Expect(launcherIP).NotTo(BeEmpty(), "Launcher pod should have an IP")

		targetURL := fmt.Sprintf("http://%s:%d", launcherIP, benchCfg.FMAModelPort)
		GinkgoWriter.Printf("FMA vLLM endpoint: %s (instance: %s, launcher: %s)\n", targetURL, instanceID, launcherName)

		By("Checking Prometheus metric availability before load")
		for _, q := range []string{
			fmt.Sprintf(`vllm:kv_cache_usage_perc{namespace="%s"}`, ns),
			fmt.Sprintf(`vllm:num_requests_waiting{namespace="%s"}`, ns),
			fmt.Sprintf(`kube_deployment_status_replicas{deployment="%s",namespace="%s"}`, fmaRes.DeploymentName, ns),
		} {
			val, qErr := QueryRangeAvg(promClient.API(), q, time.Now().Add(-2*time.Minute), time.Now(), 30*time.Second)
			if qErr != nil {
				GinkgoWriter.Printf("  Metric check: %s → NOT FOUND (%v)\n", q, qErr)
			} else {
				GinkgoWriter.Printf("  Metric check: %s → %.4f\n", q, val)
			}
		}

		By("Launching GuideLLM Load Generator against FMA vLLM endpoint")
		loadJobName := fmaRes.DeploymentName + "-load"
		_ = k8sClient.BatchV1().Jobs(ns).Delete(ctx, loadJobName, metav1.DeleteOptions{
			PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
		})
		time.Sleep(2 * time.Second)

		err = CreateGuideLLMJobWithArgs(ctx, k8sClient, ns, fmaRes.DeploymentName, targetURL, benchCfg.FMAModelID)
		Expect(err).NotTo(HaveOccurred(), "Failed to create GuideLLM load job")
		DeferCleanup(func() {
			_ = k8sClient.BatchV1().Jobs(ns).Delete(ctx, loadJobName, metav1.DeleteOptions{
				PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
			})
		})

		loadStart := time.Now()

		By("Monitoring replicas, HPA, and launcher state while GuideLLM runs")
		var timeline []ReplicaSnap
		var metricsTimeline []MetricSnap
		var maxReplicas int32 = 1
		done := make(chan error, 1)

		go func() {
			done <- WaitForJobCompletion(ctx, k8sClient, ns, loadJobName, 25*time.Minute)
		}()

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

	fmaMonitorLoop:
		for {
			select {
			case jobErr := <-done:
				if jobErr != nil {
					logs, logErr := GetJobPodLogs(ctx, k8sClient, ns, loadJobName)
					if logErr == nil {
						GinkgoWriter.Printf("\n--- GuideLLM Job Failed. Pod Logs ---\n%s\n---------------------------\n", logs)
					}
				}
				Expect(jobErr).NotTo(HaveOccurred(), "GuideLLM job failed or timed out")
				break fmaMonitorLoop
			case <-ticker.C:
				elapsed := time.Since(loadStart).Seconds()

				// Monitor FMA Deployment replicas
				deployment, depErr := k8sClient.AppsV1().Deployments(ns).Get(ctx, fmaRes.DeploymentName, metav1.GetOptions{})
				if depErr == nil {
					spec := int32(0)
					if deployment.Spec.Replicas != nil {
						spec = *deployment.Spec.Replicas
					}
					ready := deployment.Status.ReadyReplicas
					if spec > maxReplicas {
						maxReplicas = spec
					}
					timeline = append(timeline, ReplicaSnap{ElapsedSec: elapsed, SpecReplicas: spec, ReadyReplicas: ready})
					GinkgoWriter.Printf("  [%.0fs] FMA Deployment: spec=%d ready=%d\n", elapsed, spec, ready)
				}

				// Sample KV cache, vLLM queue depth from Prometheus
				snap := MetricSnap{ElapsedSec: elapsed}
				qdQuery := fmt.Sprintf(`avg(vllm:num_requests_waiting{namespace="%s"})`, ns)
				kvQuery := fmt.Sprintf(`avg(vllm:kv_cache_usage_perc{namespace="%s"})`, ns)
				eppQDQuery := fmt.Sprintf(`sum(inference_extension_flow_control_queue_size{namespace="%s"})`, ns)
				if qdResult, _, qdErr := promClient.API().Query(ctx, qdQuery, time.Now()); qdErr == nil {
					if vec, ok := qdResult.(model.Vector); ok && len(vec) > 0 {
						snap.QueueDepth = float64(vec[0].Value)
					}
				}
				if kvResult, _, kvErr := promClient.API().Query(ctx, kvQuery, time.Now()); kvErr == nil {
					if vec, ok := kvResult.(model.Vector); ok && len(vec) > 0 {
						snap.KVCache = float64(vec[0].Value)
					}
				}
				if eppResult, _, eppErr := promClient.API().Query(ctx, eppQDQuery, time.Now()); eppErr == nil {
					if vec, ok := eppResult.(model.Vector); ok && len(vec) > 0 {
						snap.EPPQueueDepth = float64(vec[0].Value)
					}
				}
				metricsTimeline = append(metricsTimeline, snap)
				GinkgoWriter.Printf("  [%.0fs] queue_depth=%.1f epp_queue=%.1f kv_cache=%.3f\n", elapsed, snap.QueueDepth, snap.EPPQueueDepth, snap.KVCache)

				// Monitor launcher state (FMA-specific)
				launchers, lErr := k8sClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
					LabelSelector: "dual-pods.llm-d.ai/launcher-config-name=" + fmaRes.LauncherConfigName,
				})
				if lErr == nil {
					sleeping := 0
					bound := 0
					for _, lp := range launchers.Items {
						if lp.Labels["dual-pods.llm-d.ai/sleeping"] == labelValueTrue {
							sleeping++
						}
						if lp.Labels["dual-pods.llm-d.ai/dual"] != "" {
							bound++
						}
					}
					GinkgoWriter.Printf("  [%.0fs] Launchers: total=%d bound=%d sleeping=%d\n", elapsed, len(launchers.Items), bound, sleeping)
				}

				// Monitor HPA
				hpaList, hpaErr := k8sClient.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
				if hpaErr == nil {
					for i := range hpaList.Items {
						h := &hpaList.Items[i]
						GinkgoWriter.Printf("  [%.0fs] HPA %s: current=%d desired=%d\n", elapsed, h.Name, h.Status.CurrentReplicas, h.Status.DesiredReplicas)
					}
				}
			}
		}
		loadEnd := time.Now()
		loadDuration := loadEnd.Sub(loadStart).Seconds()

		By("Extracting GuideLLM results from pod logs")
		logs, err := GetJobPodLogs(ctx, k8sClient, ns, loadJobName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get GuideLLM pod logs")

		var guidellmRaw json.RawMessage
		var ttftJSON, itlJSON, throughputJSON json.RawMessage

		if idx := strings.Index(logs, "=== BENCHMARK JSON ==="); idx != -1 {
			jsonStr := strings.TrimSpace(logs[idx+len("=== BENCHMARK JSON ==="):])
			guidellmRaw = json.RawMessage(jsonStr)

			var parsed map[string]interface{}
			if jsonErr := json.Unmarshal([]byte(jsonStr), &parsed); jsonErr == nil {
				extractGuideLLMMetric(&parsed, "time_to_first_token_ms", &ttftJSON)
				extractGuideLLMMetric(&parsed, "inter_token_latency_ms", &itlJSON)
				extractGuideLLMMetric(&parsed, "output_tokens_per_second", &throughputJSON)
			} else {
				GinkgoWriter.Printf("Warning: failed to parse GuideLLM JSON: %v\n", jsonErr)
			}
		} else {
			GinkgoWriter.Println("Warning: '=== BENCHMARK JSON ===' marker not found in pod logs")
		}

		By("Querying Prometheus for averages over load window")
		replicaAvg, _ := QueryRangeAvg(promClient.API(),
			fmt.Sprintf(`avg(kube_deployment_status_replicas{deployment="%s",namespace="%s"})`, fmaRes.DeploymentName, ns),
			loadStart, loadEnd, 30*time.Second)
		qdAvg, _ := QueryRangeAvg(promClient.API(),
			fmt.Sprintf(`avg(vllm:num_requests_waiting{namespace="%s"})`, ns),
			loadStart, loadEnd, 30*time.Second)
		kvAvg, _ := QueryRangeAvg(promClient.API(),
			fmt.Sprintf(`avg(vllm:kv_cache_usage_perc{namespace="%s"})`, ns),
			loadStart, loadEnd, 30*time.Second)
		eppQDAvg, _ := QueryRangeAvg(promClient.API(),
			fmt.Sprintf(`sum(inference_extension_flow_control_queue_size{namespace="%s"})`, ns),
			loadStart, loadEnd, 30*time.Second)

		fmaResults.LoadTestDurationMs = loadDuration * 1000

		GinkgoWriter.Printf("\n========================================\n")
		GinkgoWriter.Printf("  FMA LOAD BENCHMARK RESULTS\n")
		GinkgoWriter.Printf("========================================\n")
		GinkgoWriter.Printf("  Duration:        %.0fs\n", loadDuration)
		GinkgoWriter.Printf("  Max Replicas:    %d\n", maxReplicas)
		GinkgoWriter.Printf("  Avg Replicas:    %.2f\n", replicaAvg)
		GinkgoWriter.Printf("  Avg Queue Depth: %.2f\n", qdAvg)
		GinkgoWriter.Printf("  Avg EPP Queue:   %.2f\n", eppQDAvg)
		GinkgoWriter.Printf("  Avg KV Cache:    %.3f\n", kvAvg)
		if ttftJSON != nil {
			GinkgoWriter.Printf("  TTFT:            %s\n", string(ttftJSON))
		}
		if itlJSON != nil {
			GinkgoWriter.Printf("  ITL:             %s\n", string(itlJSON))
		}
		if throughputJSON != nil {
			GinkgoWriter.Printf("  Throughput:      %s\n", string(throughputJSON))
		}
		GinkgoWriter.Printf("  Replica Timeline (%d snapshots):\n", len(timeline))
		for _, s := range timeline {
			GinkgoWriter.Printf("    t=%.0fs  spec=%d  ready=%d\n", s.ElapsedSec, s.SpecReplicas, s.ReadyReplicas)
		}
		GinkgoWriter.Printf("========================================\n\n")

		// Save as PrefillResult for apples-to-apples comparison with Phase 3a
		fmaLoadResult := PrefillResult{
			AutoscalerType:   "WVA+FMA",
			ModelID:          benchCfg.FMAModelID,
			ReplicaTimeline:  timeline,
			MetricsTimeline:  metricsTimeline,
			AvgReplicas:      replicaAvg,
			MaxReplicas:      maxReplicas,
			AvgQueueDepth:    qdAvg,
			AvgEPPQueueDepth: eppQDAvg,
			AvgKVCache:       kvAvg,
			TTFT:             ttftJSON,
			ITL:              itlJSON,
			Throughput:       throughputJSON,
			GuideLLMRaw:      guidellmRaw,
			DurationSec:      loadDuration,
		}
		prefillResults = append(prefillResults, fmaLoadResult)

		By("Saving combined results to prefill results file")
		data, _ := json.MarshalIndent(prefillResults, "", "  ")
		_ = os.WriteFile(prefillResultsFile, data, 0644)
	})
})
