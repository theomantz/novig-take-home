#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

USE_NIX="true"
RUN_DEMO="false"

usage() {
  cat <<'USAGE'
Usage: scripts/test.sh [options]

Runs project validation checks:
  1. go test ./...
  2. go vet ./...
  3. go test -race ./...

Options:
  --no-nix      Run commands directly instead of through "nix develop -c".
  --with-demo   Also run "go run ./cmd/demo" after all validation checks.
  -h, --help    Show this help text.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-nix)
      USE_NIX="false"
      shift
      ;;
    --with-demo)
      RUN_DEMO="true"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ "$USE_NIX" == "true" ]]; then
  if [[ -n "${IN_NIX_SHELL:-}" ]]; then
    USE_NIX="false"
  elif ! command -v nix >/dev/null 2>&1; then
    echo "nix not found; falling back to direct Go commands. Use --no-nix to silence this message."
    USE_NIX="false"
  fi
fi

run_cmd() {
  echo "==> $*"
  "$@"
}

run_go() {
  if [[ "$USE_NIX" == "true" ]]; then
    run_cmd nix develop -c "$@"
  else
    run_cmd "$@"
  fi
}

run_go go test ./...
run_go go vet ./...
run_go go test -race ./...

if [[ "$RUN_DEMO" == "true" ]]; then
  run_go go run ./cmd/demo
fi

echo "All checks passed."
