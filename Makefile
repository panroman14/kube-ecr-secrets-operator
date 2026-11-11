# Image to build/push, e.g. make docker-build IMG=docker.io/your-username/kube-ecr-secrets-operator:0.1.0
IMG ?= kube-ecr-secrets-operator:dev
PLATFORM ?= linux/amd64
CHART = charts/kube-ecr-secrets-operator
CONTROLLER_GEN = go tool sigs.k8s.io/controller-tools/cmd/controller-gen

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

.PHONY: help
help: ## Show targets.
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  %-14s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: generate
generate: ## Regenerate DeepCopy code and the chart CRDs from api/v1alpha2.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."
	$(CONTROLLER_GEN) crd paths="./api/v1alpha2/..." output:crd:artifacts:config=$(CHART)/crds

.PHONY: fmt
fmt: ## go fmt.
	go fmt ./...

.PHONY: vet
vet: ## go vet.
	go vet ./...

.PHONY: lint
lint: ## golangci-lint.
	go tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint run

.PHONY: vuln
vuln: ## govulncheck.
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: build
build: generate fmt vet ## Build bin/manager.
	go build -trimpath -o bin/manager main.go

.PHONY: helm-lint
helm-lint: ## Lint and render the chart.
	helm lint $(CHART)
	helm template t $(CHART) --set 'pullSecrets[0].name=x,pullSecrets[0].region=eu-west-1,pullSecrets[0].credentialsSecret=c,pullSecrets[0].credentialsNamespace=default,pullSecrets[0].secretName=p,pullSecrets[0].namespaces={default}' >/dev/null

.PHONY: docker-build
docker-build: ## Build the image.
	docker build --platform $(PLATFORM) -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push the image.
	docker push $(IMG)
