#!/usr/bin/env bash
set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE="github.com/normalzzz/clientgo-learning/chapter6"
OUTPUT_PKG="${MODULE}/pkg/generated"

cd "${SCRIPT_ROOT}"

CODEGEN_PKG="$(go env GOMODCACHE)/k8s.io/code-generator@v0.35.0"
export GOBIN="${SCRIPT_ROOT}/.bin"
mkdir -p "${GOBIN}"

source "${CODEGEN_PKG}/kube_codegen.sh"

kube::codegen::gen_client \
  --with-watch \
  --output-dir "${SCRIPT_ROOT}/pkg/generated" \
  --output-pkg "${OUTPUT_PKG}" \
  --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
  "${SCRIPT_ROOT}/pkg/apis"
