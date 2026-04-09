package fixtures

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
)

// --- InferenceServerConfig (ISC) ---

// ISCOption is a functional option for configuring an InferenceServerConfig.
type ISCOption func(*fmav1alpha1.InferenceServerConfig)

// WithISCEnvVars sets additional environment variables on the ISC.
func WithISCEnvVars(envVars map[string]string) ISCOption {
	return func(isc *fmav1alpha1.InferenceServerConfig) {
		if isc.Spec.ModelServerConfig.EnvVars == nil {
			isc.Spec.ModelServerConfig.EnvVars = make(map[string]string)
		}
		for k, v := range envVars {
			isc.Spec.ModelServerConfig.EnvVars[k] = v
		}
	}
}

// WithISCSleepMode adds --enable-sleep-mode to the ISC options.
// Only use on platforms with real NVIDIA GPUs (not Kind emulator on ARM).
func WithISCSleepMode() ISCOption {
	return func(isc *fmav1alpha1.InferenceServerConfig) {
		isc.Spec.ModelServerConfig.Options += " --enable-sleep-mode"
	}
}

func buildISC(namespace, name, modelID string, port int32, lcName string, opts ...ISCOption) *fmav1alpha1.InferenceServerConfig {
	isc := &fmav1alpha1.InferenceServerConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "fma.llm-d.ai/v1alpha1",
			Kind:       "InferenceServerConfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: fmav1alpha1.InferenceServerConfigSpec{
			ModelServerConfig: fmav1alpha1.ModelServerConfig{
				Port:    port,
				Options: "--model " + modelID,
				EnvVars: map[string]string{
					"VLLM_SERVER_DEV_MODE":   "1",
					"VLLM_LOGGING_LEVEL":     "DEBUG",
					"VLLM_CPU_KVCACHE_SPACE": "1",
				},
			},
			LauncherConfigName: lcName,
		},
	}
	for _, opt := range opts {
		opt(isc)
	}
	return isc
}

// CreateISC creates an InferenceServerConfig. Fails if it already exists.
func CreateISC(ctx context.Context, crClient client.Client, namespace, name, modelID string, port int32, lcName string, opts ...ISCOption) error {
	return crClient.Create(ctx, buildISC(namespace, name, modelID, port, lcName, opts...))
}

// DeleteISC deletes an InferenceServerConfig. Idempotent.
func DeleteISC(ctx context.Context, crClient client.Client, namespace, name string) error {
	isc := &fmav1alpha1.InferenceServerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	err := crClient.Delete(ctx, isc)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete ISC %s: %w", name, err)
	}
	return nil
}

// EnsureISC creates or replaces an InferenceServerConfig.
func EnsureISC(ctx context.Context, crClient client.Client, namespace, name, modelID string, port int32, lcName string, opts ...ISCOption) error {
	_ = DeleteISC(ctx, crClient, namespace, name)
	time.Sleep(500 * time.Millisecond)
	return CreateISC(ctx, crClient, namespace, name, modelID, port, lcName, opts...)
}

// --- LauncherConfig (LC) ---

// LCOption is a functional option for configuring a LauncherConfig.
type LCOption func(*fmav1alpha1.LauncherConfig)

// WithLCServiceAccount sets the service account on the launcher pod template.
func WithLCServiceAccount(sa string) LCOption {
	return func(lc *fmav1alpha1.LauncherConfig) {
		lc.Spec.PodTemplate.Spec.ServiceAccountName = sa
	}
}

// WithLCImagePullPolicy overrides the launcher container image pull policy.
func WithLCImagePullPolicy(policy corev1.PullPolicy) LCOption {
	return func(lc *fmav1alpha1.LauncherConfig) {
		if len(lc.Spec.PodTemplate.Spec.Containers) > 0 {
			lc.Spec.PodTemplate.Spec.Containers[0].ImagePullPolicy = policy
		}
	}
}

func buildLC(namespace, name string, maxSleeping int32, launcherImage string, mockGPUs bool, opts ...LCOption) *fmav1alpha1.LauncherConfig {
	launcherArgs := "python3 launcher.py --host 0.0.0.0 --port 8001 --log-level info"
	if mockGPUs {
		launcherArgs = "python3 launcher.py --mock-gpus --host 0.0.0.0 --port 8001 --log-level info"
	}

	lc := &fmav1alpha1.LauncherConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "fma.llm-d.ai/v1alpha1",
			Kind:       "LauncherConfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: fmav1alpha1.LauncherConfigSpec{
			MaxSleepingInstances: maxSleeping,
			PodTemplate: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "testlauncher",
					Containers: []corev1.Container{
						{
							Name:            "inference-server",
							Image:           launcherImage,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/bin/bash", "-c"},
							Args:            []string{launcherArgs},
							Env: []corev1.EnvVar{
								{
									Name:      "NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}},
								},
								{
									Name:      "NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
								},
							},
						},
					},
				},
			},
		},
	}
	for _, opt := range opts {
		opt(lc)
	}
	return lc
}

// CreateLC creates a LauncherConfig. Fails if it already exists.
func CreateLC(ctx context.Context, crClient client.Client, namespace, name string, maxSleeping int32, launcherImage string, mockGPUs bool, opts ...LCOption) error {
	return crClient.Create(ctx, buildLC(namespace, name, maxSleeping, launcherImage, mockGPUs, opts...))
}

// DeleteLC deletes a LauncherConfig. Idempotent.
func DeleteLC(ctx context.Context, crClient client.Client, namespace, name string) error {
	lc := &fmav1alpha1.LauncherConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	err := crClient.Delete(ctx, lc)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete LC %s: %w", name, err)
	}
	return nil
}

// EnsureLC creates or replaces a LauncherConfig.
func EnsureLC(ctx context.Context, crClient client.Client, namespace, name string, maxSleeping int32, launcherImage string, mockGPUs bool, opts ...LCOption) error {
	_ = DeleteLC(ctx, crClient, namespace, name)
	time.Sleep(500 * time.Millisecond)
	return CreateLC(ctx, crClient, namespace, name, maxSleeping, launcherImage, mockGPUs, opts...)
}

// --- LauncherPopulationPolicy (LPP) ---

// LPPOption is a functional option for configuring a LauncherPopulationPolicy.
type LPPOption func(*fmav1alpha1.LauncherPopulationPolicy)

func buildLPP(namespace, name, lcName string, count int32, opts ...LPPOption) *fmav1alpha1.LauncherPopulationPolicy {
	lpp := &fmav1alpha1.LauncherPopulationPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "fma.llm-d.ai/v1alpha1",
			Kind:       "LauncherPopulationPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: fmav1alpha1.LauncherPopulationPolicySpec{
			EnhancedNodeSelector: fmav1alpha1.EnhancedNodeSelector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"nvidia.com/gpu.present": "true",
					},
				},
			},
			CountForLauncher: []fmav1alpha1.CountForLauncher{
				{
					LauncherConfigName: lcName,
					LauncherCount:      count,
				},
			},
		},
	}
	for _, opt := range opts {
		opt(lpp)
	}
	return lpp
}

// CreateLPP creates a LauncherPopulationPolicy. Fails if it already exists.
func CreateLPP(ctx context.Context, crClient client.Client, namespace, name, lcName string, count int32, opts ...LPPOption) error {
	return crClient.Create(ctx, buildLPP(namespace, name, lcName, count, opts...))
}

// DeleteLPP deletes a LauncherPopulationPolicy. Idempotent.
func DeleteLPP(ctx context.Context, crClient client.Client, namespace, name string) error {
	lpp := &fmav1alpha1.LauncherPopulationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	err := crClient.Delete(ctx, lpp)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete LPP %s: %w", name, err)
	}
	return nil
}

// EnsureLPP creates or replaces a LauncherPopulationPolicy.
func EnsureLPP(ctx context.Context, crClient client.Client, namespace, name, lcName string, count int32, opts ...LPPOption) error {
	_ = DeleteLPP(ctx, crClient, namespace, name)
	time.Sleep(500 * time.Millisecond)
	return CreateLPP(ctx, crClient, namespace, name, lcName, count, opts...)
}

// --- FMA ReplicaSet (requester pod) ---

// FMAReplicaSetOption is a functional option for configuring the FMA requester ReplicaSet.
type FMAReplicaSetOption func(*appsv1.ReplicaSet)

// WithFMANodeSelector sets a nodeSelector on the requester pod template.
func WithFMANodeSelector(nodeSelector map[string]string) FMAReplicaSetOption {
	return func(rs *appsv1.ReplicaSet) {
		rs.Spec.Template.Spec.NodeSelector = nodeSelector
	}
}

// WithFMAImagePullPolicy overrides the requester container image pull policy.
func WithFMAImagePullPolicy(policy corev1.PullPolicy) FMAReplicaSetOption {
	return func(rs *appsv1.ReplicaSet) {
		if len(rs.Spec.Template.Spec.Containers) > 0 {
			rs.Spec.Template.Spec.Containers[0].ImagePullPolicy = policy
		}
	}
}

func buildFMAReplicaSet(namespace, name, iscName, requesterImage string, replicas int32, opts ...FMAReplicaSetOption) *appsv1.ReplicaSet {
	labels := map[string]string{
		"app":      "fma-benchmark",
		"instance": name,
	}

	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						"dual-pods.llm-d.ai/admin-port":              "8081",
						"dual-pods.llm-d.ai/inference-server-config": iscName,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "testreq",
					Containers: []corev1.Container{
						{
							Name:            "inference-server",
							Image:           requesterImage,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/ko-app/test-requester"},
							Args: []string{
								"--node=$(NODE_NAME)",
								"--pod-uid=$(POD_UID)",
								"--namespace=$(NAMESPACE)",
							},
							Env: []corev1.EnvVar{
								{
									Name:      "NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}},
								},
								{
									Name:      "POD_UID",
									ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}},
								},
								{
									Name:      "NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
								},
							},
							Ports: []corev1.ContainerPort{
								{Name: "probes", ContainerPort: 8080},
								{Name: "spi", ContainerPort: 8081},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/ready",
										Port: intstr.FromInt32(8080),
									},
								},
								InitialDelaySeconds: 2,
								PeriodSeconds:       5,
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									"nvidia.com/gpu":      resource.MustParse("1"),
									corev1.ResourceCPU:    resource.MustParse("200m"),
									corev1.ResourceMemory: resource.MustParse("250Mi"),
								},
							},
						},
					},
				},
			},
		},
	}
	for _, opt := range opts {
		opt(rs)
	}
	return rs
}

// CreateFMAReplicaSet creates the FMA requester ReplicaSet.
func CreateFMAReplicaSet(ctx context.Context, k8sClient kubernetes.Interface, namespace, name, iscName, requesterImage string, replicas int32, opts ...FMAReplicaSetOption) error {
	rs := buildFMAReplicaSet(namespace, name, iscName, requesterImage, replicas, opts...)
	_, err := k8sClient.AppsV1().ReplicaSets(namespace).Create(ctx, rs, metav1.CreateOptions{})
	return err
}

// DeleteFMAReplicaSet deletes the FMA requester ReplicaSet. Idempotent.
func DeleteFMAReplicaSet(ctx context.Context, k8sClient kubernetes.Interface, namespace, name string) error {
	err := k8sClient.AppsV1().ReplicaSets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete FMA RS %s: %w", name, err)
	}
	return nil
}

// ScaleFMAReplicaSet patches the replica count of the FMA requester ReplicaSet.
func ScaleFMAReplicaSet(ctx context.Context, k8sClient kubernetes.Interface, namespace, name string, replicas int32) error {
	scale, err := k8sClient.AppsV1().ReplicaSets(namespace).GetScale(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get scale for RS %s: %w", name, err)
	}
	scale.Spec.Replicas = replicas
	_, err = k8sClient.AppsV1().ReplicaSets(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("scale RS %s to %d: %w", name, replicas, err)
	}
	return nil
}

// --- Wait helpers ---

// WaitForFMALaunchers waits until at least `count` launcher pods with the given LC label are Ready.
func WaitForFMALaunchers(ctx context.Context, k8sClient kubernetes.Interface, namespace, lcName string, count int, timeout time.Duration) error {
	label := "dual-pods.llm-d.ai/launcher-config-name=" + lcName
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pods, err := k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: label})
		if err != nil {
			return fmt.Errorf("list launcher pods: %w", err)
		}
		ready := 0
		for _, pod := range pods.Items {
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					ready++
				}
			}
		}
		if ready >= count {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %d ready launcher pods (label: %s)", count, label)
}

// WaitForFMARequesterReady waits until the RS has the desired number of ready replicas.
func WaitForFMARequesterReady(ctx context.Context, k8sClient kubernetes.Interface, namespace, rsName string, replicas int32, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rs, err := k8sClient.AppsV1().ReplicaSets(namespace).Get(ctx, rsName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get RS %s: %w", rsName, err)
		}
		if rs.Status.ReadyReplicas >= replicas {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timed out waiting for RS %s to have %d ready replicas", rsName, replicas)
}

// WaitForFMAScaleDown waits until the RS has 0 replicas.
func WaitForFMAScaleDown(ctx context.Context, k8sClient kubernetes.Interface, namespace, rsName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rs, err := k8sClient.AppsV1().ReplicaSets(namespace).Get(ctx, rsName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get RS %s: %w", rsName, err)
		}
		if rs.Status.Replicas == 0 {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timed out waiting for RS %s to scale down to 0", rsName)
}

// --- RBAC and prerequisites ---

// EnsureFMARBAC creates the service accounts, roles, and role bindings needed
// for FMA launcher and requester pods. Matches the RBAC setup in FMA's
// test/e2e/run-launcher-based.sh.
func EnsureFMARBAC(ctx context.Context, k8sClient kubernetes.Interface, namespace string) error {
	// Create service accounts
	for _, saName := range []string{"testreq", "testlauncher"} {
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: namespace},
		}
		_, err := k8sClient.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("create SA %s: %w", saName, err)
		}
	}

	// Role for requester — matches FMA's test/e2e/run-launcher-based.sh testreq role
	reqRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "fma-requester-role", Namespace: namespace},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"fma.llm-d.ai"},
				Resources: []string{"inferenceserverconfigs", "launcherconfigs"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups:     []string{""},
				Resources:     []string{"configmaps"},
				ResourceNames: []string{"gpu-map", "gpu-allocs"},
				Verbs:         []string{"get", "list", "watch", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
	_, err := k8sClient.RbacV1().Roles(namespace).Create(ctx, reqRole, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create requester role: %w", err)
	}

	reqBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "fma-requester-binding", Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "fma-requester-role",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "testreq", Namespace: namespace},
		},
	}
	_, err = k8sClient.RbacV1().RoleBindings(namespace).Create(ctx, reqBinding, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create requester role binding: %w", err)
	}

	// Role for launcher: read configmaps + patch pods (for self-annotation)
	launcherRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "fma-launcher-role", Namespace: namespace},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "patch"},
			},
		},
	}
	_, err = k8sClient.RbacV1().Roles(namespace).Create(ctx, launcherRole, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create launcher role: %w", err)
	}

	launcherBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "fma-launcher-binding", Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "fma-launcher-role",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "testlauncher", Namespace: namespace},
		},
	}
	_, err = k8sClient.RbacV1().RoleBindings(namespace).Create(ctx, launcherBinding, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create launcher role binding: %w", err)
	}

	return nil
}

// EnsureFMAGPUMap creates the gpu-map ConfigMap with fake GPU mappings for a Kind cluster.
// nodeName is the node where FMA pods will run.
func EnsureFMAGPUMap(ctx context.Context, k8sClient kubernetes.Interface, namespace string, nodeName string, gpuCount int) error {
	// Build fake GPU mapping: {"GPU-0": 0, "GPU-1": 1, ...}
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < gpuCount; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("\"GPU-" + strconv.Itoa(i) + "\": " + strconv.Itoa(i))
	}
	b.WriteString("}")
	mapping := b.String()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-map", Namespace: namespace},
		Data:       map[string]string{nodeName: mapping},
	}

	existing, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(ctx, "gpu-map", metav1.GetOptions{})
	if err == nil {
		existing.Data = cm.Data
		_, err = k8sClient.CoreV1().ConfigMaps(namespace).Update(ctx, existing, metav1.UpdateOptions{})
		return err
	}
	if errors.IsNotFound(err) {
		_, err = k8sClient.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
		return err
	}
	return err
}
