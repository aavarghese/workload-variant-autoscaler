#!/usr/bin/env bash
#
# FMA (Fast Model Actuation) deployment functions.
# Handles the complete FMA setup: image builds (Kind), GPU labels, gpu-map,
# RBAC, and controller deployment via FMA's deploy_fma.sh.
#
# Requires vars: FMA_REPO_PATH, FMA_NAMESPACE, FMA_CHART_INSTANCE_NAME,
#                FMA_IMAGE_REGISTRY, FMA_IMAGE_TAG
# Optional vars: KIND_CLUSTER_NAME (for Kind image loading)

deploy_fma_controllers() {
    log_info "Deploying FMA (Fast Model Actuation)..."

    if [ -z "$FMA_REPO_PATH" ]; then
        log_error "FMA_REPO_PATH must be set when DEPLOY_FMA=true"
        exit 1
    fi
    if [ ! -f "$FMA_REPO_PATH/test/e2e/deploy_fma.sh" ]; then
        log_error "FMA deploy script not found at $FMA_REPO_PATH/test/e2e/deploy_fma.sh"
        exit 1
    fi

    # --- Step 1: Build and load images (Kind emulator only) ---
    if [ "$ENVIRONMENT" = "kind-emulator" ]; then
        log_info "Building FMA images locally for Kind..."
        pushd "$FMA_REPO_PATH" > /dev/null

        # Check for ko (required for building requester/controller images)
        if ! command -v ko &> /dev/null; then
            log_warning "ko not found — skipping local image builds. Pre-built images must be available."
        else
            make build-test-requester-local build-test-launcher-local build-controller-local build-populator-local

            local cluster_name="${KIND_CLUSTER_NAME:-kind-wva-gpu-cluster}"
            log_info "Loading FMA images into Kind cluster: $cluster_name"
            make load-test-requester-local load-test-launcher-local load-controller-local load-populator-local CLUSTER_NAME="$cluster_name"

            # Export image names for benchmark config
            FMA_LAUNCHER_IMAGE=$(make echo-var VAR=TEST_LAUNCHER_IMG)
            FMA_REQUESTER_IMAGE=$(make echo-var VAR=TEST_REQUESTER_IMG)
            export FMA_LAUNCHER_IMAGE FMA_REQUESTER_IMAGE
            log_info "FMA images: launcher=$FMA_LAUNCHER_IMAGE requester=$FMA_REQUESTER_IMAGE"
        fi

        popd > /dev/null
    fi

    # --- Step 2: GPU node setup ---
    if [ "$ENVIRONMENT" = "kind-emulator" ]; then
        # Kind: label nodes with fake GPU info and create gpu-map ConfigMap
        log_info "Labeling Kind nodes with GPU labels..."
        for node in $(kubectl get nodes -o name | sed 's%^node/%%'); do
            kubectl label node "$node" nvidia.com/gpu.present=true nvidia.com/gpu.product=NVIDIA-L40S nvidia.com/gpu.count=2 --overwrite=true
        done

        log_info "Creating gpu-map ConfigMap with fake GPU mappings..."
        kubectl create cm gpu-map -n "$FMA_NAMESPACE" 2>/dev/null || true
        for node in $(kubectl get nodes -o name | sed 's%^node/%%'); do
            kubectl patch cm gpu-map -n "$FMA_NAMESPACE" --type=merge \
                -p="{\"data\":{\"$node\":\"{\\\"GPU-0\\\": 0, \\\"GPU-1\\\": 1}\"}}"
        done
    else
        # Real cluster: GPU labels should already exist (GPU Operator).
        # Verify at least one GPU node exists.
        local gpu_nodes
        gpu_nodes=$(kubectl get nodes -l nvidia.com/gpu.present=true -o name 2>/dev/null | wc -l | tr -d ' ')
        if [ "${gpu_nodes:-0}" -eq 0 ]; then
            log_warning "No nodes with nvidia.com/gpu.present=true found. FMA launcher population may fail."
        else
            log_info "Found $gpu_nodes GPU node(s)"
        fi
    fi

    # --- Step 3: RBAC setup ---
    log_info "Setting up FMA RBAC (service accounts, roles, role bindings)..."
    kubectl create sa testreq -n "$FMA_NAMESPACE" 2>/dev/null || true
    kubectl create sa testlauncher -n "$FMA_NAMESPACE" 2>/dev/null || true

    # Requester role: FMA CRDs + configmaps (gpu-map read/write) + pods (discovery)
    kubectl apply -n "$FMA_NAMESPACE" -f - <<'RBACEOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: testreq
rules:
- apiGroups: ["fma.llm-d.ai"]
  resources: ["inferenceserverconfigs", "launcherconfigs"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resourceNames: ["gpu-map", "gpu-allocs"]
  resources: ["configmaps"]
  verbs: ["update", "patch", "get", "list", "watch"]
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["create"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch"]
RBACEOF
    kubectl create rolebinding testreq --role=testreq \
        --serviceaccount="$FMA_NAMESPACE":testreq -n "$FMA_NAMESPACE" 2>/dev/null || true

    # Launcher role: configmaps (gpu-map read) + pods (self-annotation patch)
    kubectl apply -n "$FMA_NAMESPACE" -f - <<'RBACEOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: testlauncher
rules:
- apiGroups: [""]
  resourceNames: ["gpu-map"]
  resources: ["configmaps"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "patch"]
RBACEOF
    kubectl create rolebinding testlauncher --role=testlauncher \
        --serviceaccount="$FMA_NAMESPACE":testlauncher -n "$FMA_NAMESPACE" 2>/dev/null || true

    # --- Step 4: Deploy FMA controllers via Helm ---
    log_info "Deploying FMA controllers via deploy_fma.sh..."
    pushd "$FMA_REPO_PATH" > /dev/null

    local helm_extra="${FMA_HELM_EXTRA_ARGS:-}"
    if [ "$ENVIRONMENT" = "kind-emulator" ]; then
        helm_extra="${helm_extra} --set global.local=true"
    fi

    FMA_NAMESPACE="$FMA_NAMESPACE" \
    FMA_CHART_INSTANCE_NAME="$FMA_CHART_INSTANCE_NAME" \
    CONTAINER_IMG_REG="$FMA_IMAGE_REGISTRY" \
    IMAGE_TAG="$FMA_IMAGE_TAG" \
    NODE_VIEW_CLUSTER_ROLE="${FMA_NODE_VIEW_CLUSTER_ROLE:-create/please}" \
    HELM_EXTRA_ARGS="$helm_extra" \
    bash test/e2e/deploy_fma.sh
    popd > /dev/null

    log_success "FMA deployment complete"
}

undeploy_fma_controllers() {
    log_info "Uninstalling FMA controllers..."

    helm uninstall "$FMA_CHART_INSTANCE_NAME" -n "$FMA_NAMESPACE" 2>/dev/null || \
        log_warning "FMA not found or already uninstalled"

    # Remove RBAC
    kubectl delete rolebinding testreq testlauncher -n "$FMA_NAMESPACE" --ignore-not-found 2>/dev/null || true
    kubectl delete role testreq testlauncher -n "$FMA_NAMESPACE" --ignore-not-found 2>/dev/null || true
    kubectl delete sa testreq testlauncher -n "$FMA_NAMESPACE" --ignore-not-found 2>/dev/null || true

    # Remove gpu-map
    kubectl delete cm gpu-map -n "$FMA_NAMESPACE" --ignore-not-found 2>/dev/null || true

    # Remove FMA CRDs
    kubectl delete crd inferenceserverconfigs.fma.llm-d.ai --ignore-not-found 2>/dev/null || true
    kubectl delete crd launcherconfigs.fma.llm-d.ai --ignore-not-found 2>/dev/null || true
    kubectl delete crd launcherpopulationpolicies.fma.llm-d.ai --ignore-not-found 2>/dev/null || true

    log_success "FMA controllers uninstalled"
}
