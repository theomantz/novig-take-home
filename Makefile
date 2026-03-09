SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

USE_NIX ?= auto
NIX_AVAILABLE := $(shell command -v nix >/dev/null 2>&1 && echo true || echo false)
IN_NIX_SHELL_ACTIVE := $(if $(strip $(IN_NIX_SHELL)),true,false)
NIX_DISABLED := $(if $(filter false FALSE 0 no NO,$(USE_NIX)),true,false)

ifeq ($(IN_NIX_SHELL_ACTIVE),true)
GO_PREFIX :=
else ifeq ($(NIX_AVAILABLE),true)
ifneq ($(NIX_DISABLED),true)
GO_PREFIX := nix develop -c
endif
endif

.PHONY: help test test-with-demo unit-test vet test-race demo

help: ; @printf '%s\n' 'Usage: make <target> [USE_NIX=auto|true|false]' '' 'Targets:' '  test            Run go test, go vet, and go test -race.' '  test-with-demo  Run test, then go run ./cmd/demo.' '  unit-test       Run go test ./...' '  vet             Run go vet ./...' '  test-race       Run go test -race ./...' '  demo            Run go run ./cmd/demo.' '  help            Show this help.' '' 'Nix behavior:' '  USE_NIX=auto (default): use nix develop -c when nix is installed and not already in nix shell.' '  USE_NIX=false: run Go commands directly.'

test: unit-test vet test-race

test-with-demo: test demo

unit-test: ; @echo "==> go test ./..."; $(GO_PREFIX) go test ./...

vet: ; @echo "==> go vet ./..."; $(GO_PREFIX) go vet ./...

test-race: ; @echo "==> go test -race ./..."; $(GO_PREFIX) go test -race ./...

demo: ; @echo "==> go run ./cmd/demo"; $(GO_PREFIX) go run ./cmd/demo
