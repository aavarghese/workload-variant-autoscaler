#!/usr/bin/env bash
#
# FMA (Fast Model Actuation) deployment functions.
# Deploys FMA CRDs and controllers by calling FMA's own deploy_fma.sh script.
#
# Requires vars: FMA_REPO_PATH, FMA_NAMESPACE, FMA_CHART_INSTANCE_NAME,
#                FMA_IMAGE_REGISTRY, FMA_IMAGE_TAG

deploy_fma_controllers() {
    log_info "Deploying FMA (Fast Model Actuation) controllers..."

    if [ -z "$FMA_REPO_PATH" ]; then
        log_error "FMA_REPO_PATH must be set when DEPLOY_FMA=true"
        exit 1
    fi
    if [ ! -f "$FMA_REPO_PATH/test/e2e/deploy_fma.sh" ]; then
        log_error "FMA deploy script not found at $FMA_REPO_PATH/test/e2e/deploy_fma.sh"
        exit 1
    fi

    pushd "$FMA_REPO_PATH" > /dev/null
    FMA_NAMESPACE="$FMA_NAMESPACE" \
    FMA_CHART_INSTANCE_NAME="$FMA_CHART_INSTANCE_NAME" \
    CONTAINER_IMG_REG="$FMA_IMAGE_REGISTRY" \
    IMAGE_TAG="$FMA_IMAGE_TAG" \
    NODE_VIEW_CLUSTER_ROLE="${FMA_NODE_VIEW_CLUSTER_ROLE:-create/please}" \
    HELM_EXTRA_ARGS="${FMA_HELM_EXTRA_ARGS:-}" \
    bash test/e2e/deploy_fma.sh
    popd > /dev/null

    log_success "FMA controllers deployed successfully"
}

undeploy_fma_controllers() {
    log_info "Uninstalling FMA controllers..."

    helm uninstall "$FMA_CHART_INSTANCE_NAME" -n "$FMA_NAMESPACE" 2>/dev/null || \
        log_warning "FMA not found or already uninstalled"

    # Remove FMA CRDs
    kubectl delete crd inferenceserverconfigs.fma.llm-d.ai --ignore-not-found 2>/dev/null || true
    kubectl delete crd launcherconfigs.fma.llm-d.ai --ignore-not-found 2>/dev/null || true
    kubectl delete crd launcherpopulationpolicies.fma.llm-d.ai --ignore-not-found 2>/dev/null || true

    log_success "FMA controllers uninstalled"
}
