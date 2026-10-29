#!/usr/bin/env bash
set -euo pipefail
mkdir -p .run data
go build -o .run/demo-api ./cmd/demo-api
go build -o .run/control-plane ./cmd/control-plane
go build -o .run/herd-sim ./cmd/herd-sim
ulimit -n 65535 2>/dev/null || echo "warning: could not raise open-file limit"
./.run/demo-api      > .run/demo-api.log 2>&1      & echo $! > .run/demo-api.pid
./.run/control-plane > .run/control-plane.log 2>&1 & echo $! > .run/control-plane.pid
sleep 1
./.run/herd-sim      > .run/herd-sim.log 2>&1      & echo $! > .run/herd-sim.pid
echo "started: demo-api :8080, control-plane :8090, herd-sim :8070 (logs in .run/)"
