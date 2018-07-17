#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

vendor/k8s.io/code-generator/generate-groups.sh \
deepcopy \
github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/generated \
github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/apis \
app:v1alpha1 \
--go-header-file "./tmp/codegen/boilerplate.go.txt"
