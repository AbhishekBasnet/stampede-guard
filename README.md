# Stampede Guard

Protects a backend from the thundering herd that a mass push notification creates, and measures how well it works.

A campaign push wakes thousands of devices within seconds, and each one hits the API at once. Stampede Guard attacks this from three sides:

- **Server side:** `pkg/guard` is admission middleware. It scores headroom from latency and queue depth, moves through `NORMAL → ELEVATED → BROWNOUT → SHED`, and sheds low-priority requests with `Retry-After`.
- **Sender side:** the control plane sends pushes through an outbox-backed dispatcher. An AIMD pacer, capped by a predictive limit (learned requests-per-push and deadline pressure), reads the target's health snapshot and slows down before the backend does.
- **Client side:** `pkg/retry` provides full-jitter backoff and a retry budget. The guard sends the policy to clients in `X-Fleet-Policy`.

## Layout

| Path | |
|---|---|
| `pkg/guard` | admission middleware, mode machine, health snapshot |
| `pkg/policy`, `pkg/retry`, `pkg/clock` | fleet protocol v1, backoff and budget, injectable clock |
| `internal/demoapi` | target service with chaos injection and a simulated DB pool |
| `internal/controlplane` | campaigns (SQLite), health poller, FCM provider, pacer, dispatcher, HTTP API |
| `internal/herdsim` | 20k virtual devices, naive or guarded clients, fake FCM |
| `internal/experiment` | scenarios S1–S7, metrics, charts, `REPORT.md` |

## Run

```
make up                  # demo-api :8080, control-plane :8090, herd-sim :8070
make experiment          # all scenarios, writes results/<timestamp>/
make experiment SCENARIO=S3
go run ./cmd/experiment -list   # S1–S7: push pacing, DB slowdown, aligned sync
make down
```

`make race` runs vet and `go test -race ./...`. `make e2e` runs the in-process end-to-end test. Admin endpoints use `Authorization: Bearer $ADMIN_TOKEN` (default `dev-admin-token`).

## Design notes

- Delivery is at-least-once: results are written through a transactional outbox, and running campaigns resume after a crash.
- The pacer uses the target's own health snapshot rather than client-side guesses, and the adaptive pacer drops to its minimum rate when the snapshot is stale.
- No global state or `init()`. Time goes through `clock.Clock`, so the dispatcher and experiment runner are tested with fake clocks.
