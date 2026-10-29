#!/usr/bin/env bash
set -euo pipefail
shopt -s nullglob
for pidfile in .run/*.pid; do
    pid=$(cat "$pidfile")
    if kill -0 "$pid" 2>/dev/null; then
        kill "$pid"
    fi
    rm -f "$pidfile"
done
echo "stopped"
