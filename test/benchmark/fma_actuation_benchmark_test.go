package benchmark

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/test/e2e/fixtures"
)

// FMAScenarioResources holds names of FMA objects created during benchmark setup.
type FMAScenarioResources struct {
	ISCName            string
	LauncherConfigName string
	LPPName            string
	ReplicaSetName     string
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
			ReplicaSetName:     "bench-requester",
		}

		ns := benchCfg.FMANamespace
		mockGPUs := benchCfg.Environment == "kind-emulator"

		// Set up FMA prerequisites for Kind emulator:
		// - Label GPU nodes with nvidia.com/gpu.present=true (required by FMA's LPP)
		// - Create gpu-map ConfigMap with fake GPU mappings
		// - Create service accounts and RBAC for launcher and requester pods
		By("Labeling GPU nodes with nvidia.com/gpu.present=true")
		nodes, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		for _, node := range nodes.Items {
			if qty, ok := node.Status.Allocatable["nvidia.com/gpu"]; ok && !qty.IsZero() {
				if node.Labels["nvidia.com/gpu.present"] != "true" {
					node.Labels["nvidia.com/gpu.present"] = "true"
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

		By("Creating requester ReplicaSet at 0 replicas")
		rsOpts := []fixtures.FMAReplicaSetOption{}
		if mockGPUs {
			rsOpts = append(rsOpts, fixtures.WithFMAImagePullPolicy(corev1.PullIfNotPresent))
		}
		err = fixtures.CreateFMAReplicaSet(ctx, k8sClient, ns, fmaRes.ReplicaSetName,
			fmaRes.ISCName, benchCfg.FMARequesterImage, 0, rsOpts...)
		Expect(err).NotTo(HaveOccurred(), "Failed to create requester RS")

		DeferCleanup(func() {
			_ = fixtures.DeleteFMAReplicaSet(ctx, k8sClient, ns, fmaRes.ReplicaSetName)
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
			LabelSelector: fmt.Sprintf("dual-pods.llm-d.ai/launcher-config-name=%s", fmaRes.LauncherConfigName),
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

			err := fixtures.ScaleFMAReplicaSet(ctx, k8sClient, ns, fmaRes.ReplicaSetName, 1)
			Expect(err).NotTo(HaveOccurred())

			err = fixtures.WaitForFMARequesterReady(ctx, k8sClient, ns, fmaRes.ReplicaSetName, 1, iterTimeout)
			Expect(err).NotTo(HaveOccurred(), "Requester should become ready")

			actuationMs := float64(time.Since(iterStart).Milliseconds())
			fmaResults.ColdActuationTimesMs = append(fmaResults.ColdActuationTimesMs, actuationMs)
			GinkgoWriter.Printf("  Cold iteration %d: %.0f ms\n", i+1, actuationMs)

			By(fmt.Sprintf("Cold iteration %d/%d: scale 1→0", i+1, halfIterations))
			err = fixtures.ScaleFMAReplicaSet(ctx, k8sClient, ns, fmaRes.ReplicaSetName, 0)
			Expect(err).NotTo(HaveOccurred())

			err = fixtures.WaitForFMAScaleDown(ctx, k8sClient, ns, fmaRes.ReplicaSetName, iterTimeout)
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
			By(fmt.Sprintf("Warm iteration %d/%d: scale 0→1", i+1, remainingIterations))
			iterStart := time.Now()

			err := fixtures.ScaleFMAReplicaSet(ctx, k8sClient, ns, fmaRes.ReplicaSetName, 1)
			Expect(err).NotTo(HaveOccurred())

			err = fixtures.WaitForFMARequesterReady(ctx, k8sClient, ns, fmaRes.ReplicaSetName, 1, iterTimeout)
			Expect(err).NotTo(HaveOccurred(), "Requester should become ready")

			actuationMs := float64(time.Since(iterStart).Milliseconds())

			// Classify as warm hit or cold start based on timing.
			// A warm wake-up (sleeping instance reused) is significantly faster than a cold start.
			// Use the average cold actuation time as a threshold — if under half that, it's a hit.
			coldAvg := mean(fmaResults.ColdActuationTimesMs)
			threshold := coldAvg * 0.5
			if threshold > 0 && actuationMs < threshold {
				fmaResults.WarmActuationTimesMs = append(fmaResults.WarmActuationTimesMs, actuationMs)
				GinkgoWriter.Printf("  Warm iteration %d: %.0f ms (HIT — sleeping instance woken)\n", i+1, actuationMs)
			} else {
				fmaResults.ColdActuationTimesMs = append(fmaResults.ColdActuationTimesMs, actuationMs)
				GinkgoWriter.Printf("  Warm iteration %d: %.0f ms (MISS — cold start)\n", i+1, actuationMs)
			}

			By(fmt.Sprintf("Warm iteration %d/%d: scale 1→0", i+1, remainingIterations))
			err = fixtures.ScaleFMAReplicaSet(ctx, k8sClient, ns, fmaRes.ReplicaSetName, 0)
			Expect(err).NotTo(HaveOccurred())

			err = fixtures.WaitForFMAScaleDown(ctx, k8sClient, ns, fmaRes.ReplicaSetName, iterTimeout)
			Expect(err).NotTo(HaveOccurred(), "RS should scale down to 0")

			if i < remainingIterations-1 {
				time.Sleep(cooldown)
			}
		}
	})
})
