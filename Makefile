GOBIN ?= $(shell pwd)/bin
CONTROLLER_GEN = $(GOBIN)/controller-gen

.PHONY: build test lint generate manifests

build:
	go build -o bin/kato ./cmd/kato

test:
	go test ./... -count=1

$(CONTROLLER_GEN):
	GOBIN=$(GOBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.2

generate: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object:headerFile="" paths="./api/..."

manifests: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

ENVTEST = $(GOBIN)/setup-envtest
ENVTEST_K8S_VERSION = 1.32.0

$(ENVTEST):
	GOBIN=$(GOBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.20

.PHONY: test-integration
test-integration: $(ENVTEST)
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test ./internal/controller/... -count=1
