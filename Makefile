GOBIN ?= $(shell pwd)/bin
CONTROLLER_GEN = $(GOBIN)/controller-gen
ENVTEST = $(GOBIN)/setup-envtest
ENVTEST_K8S_VERSION = 1.32.0
GOLANGCI_LINT = $(GOBIN)/golangci-lint

.PHONY: help build test test-integration lint generate manifests \
        install-crds uninstall-crds run

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: ## Build the kato binary into ./bin
	go build -o bin/kato ./cmd/kato

test: ## Run all unit tests
	go test ./... -count=1

$(CONTROLLER_GEN):
	GOBIN=$(GOBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.2

generate: $(CONTROLLER_GEN) ## Regenerate deepcopy code (api/v1alpha1/zz_generated.deepcopy.go)
	$(CONTROLLER_GEN) object:headerFile="" paths="./api/..."

manifests: $(CONTROLLER_GEN) ## Regenerate CRD manifests into config/crd/bases
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

$(ENVTEST):
	GOBIN=$(GOBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.20

test-integration: $(ENVTEST) ## Run the envtest controller integration suite
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test ./internal/controller/... -count=1

$(GOLANGCI_LINT):
	GOBIN=$(GOBIN) go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.5

lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run

install-crds: manifests ## Apply kato CRDs to the cluster in your current kubecontext
	kubectl apply -f config/crd/bases

uninstall-crds: ## Delete kato CRDs from the cluster
	kubectl delete -f config/crd/bases --ignore-not-found

run: ## Run kato locally against your kubeconfig (loads .env if present)
	@set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/kato
