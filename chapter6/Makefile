MODULE := github.com/normalzzz/clientgo-learning/chapter6
API_PKG := $(MODULE)/pkg/apis
OUTPUT_PKG := $(MODULE)/pkg/generated
GROUP_VERSION := apps:v1alpha1

export GOCACHE ?= $(CURDIR)/.gocache
export GOMODCACHE ?= $(CURDIR)/.gomodcache

.PHONY: generate crd verify clean-generated

generate: crd
	bash ./hack/update-codegen.sh

crd:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen \
		object:headerFile=./hack/boilerplate.go.txt \
		crd:crdVersions=v1 \
		paths=./pkg/apis/... \
		output:crd:artifacts:config=./config/crd/bases

verify:
	go test ./...

clean-generated:
	rm -rf ./config/crd ./pkg/generated ./pkg/apis/apps/v1alpha1/zz_generated.deepcopy.go
