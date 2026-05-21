# Webhook Handler | Resonate Go SDK

Exactly-once webhook processing via event-ID-as-promise-ID deduplication — no deduplication table, no Redis lock.

> Heads up — `resonate-sdk-go` is pre-release. The SDK has no semver tag yet, so this example pins to a specific commit. Expect API changes until `v0.1.0`.

## What this demonstrates

When a webhook provider (Stripe, GitHub, Twilio, …) retries a delivery after a network timeout, your server receives the same event twice. Without a deduplication layer, the handler runs twice and the customer gets charged twice.

Resonate solves this at the programming-model level:

- Map the `event_id` directly to a Resonate promise ID.
- Call `fn.Run(ctx, "webhook/"+event.EventID, event)` from the HTTP handler.
- If the same `event_id` arrives again, `Run` finds the existing promise and returns its cached result — **the handler body never executes a second time**.

No extra state, no database table, no distributed lock required. The deduplication window is the promise TTL (default 60 seconds, configurable).

## The code

```go
// The event_id IS the promise ID — the deduplication key.
promiseID := "webhook/" + event.EventID
h, err := processPaymentFn.Run(req.Context(), promiseID, event)
if err != nil { /* ... */ }

result, err := h.Result(req.Context())
```

A second `Run` call with the same `promiseID` skips execution and returns the cached `result` from the first call.

## Prerequisites

- Go 1.22+
- No Resonate server needed for the default `localnet` run mode.

To run against a live server instead:

```sh
brew install resonatehq/tap/resonate
resonate dev
```

Other install paths: <https://docs.resonatehq.io/get-started/install>.

## Setup

```sh
git clone https://github.com/resonatehq-examples/example-webhook-handler-go.git
cd example-webhook-handler-go
go mod download
```

## Run it

```sh
go run .
```

To run against a live Resonate server (start `resonate dev` first):

```sh
go run . -url=http://localhost:8001
```

## What to look for

Expected output:

```
=== Webhook Handler Demo ===
Mode: DEDUPLICATION (same webhook sent twice, processed once)

--- First delivery of evt_1234567890 (amount=$100) ---
[server]  received evt_1234567890 (amount=$100) — calling Run
[handler] charging $100 for evt_1234567890 (customer cus_alice)
[server]  returned: charged $100
[demo]    first result:  charged $100 (status: charged)

--- Stripe retries evt_1234567890 (simulating network timeout — amount=$999) ---
[server]  received evt_1234567890 (amount=$999) — calling Run
[server]  returned: charged $100
[demo]    second result: charged $100 (status: charged)

=== PASS: deduplication worked ===
Both deliveries returned $100 — the $999 retry payload was ignored.
The handler body ran exactly once. No duplicate charge.
```

Key things to notice:

- `[handler]` logs appear **once** — the second delivery never enters the handler body.
- The second result returns `$100`, not `$999` — the tampered retry payload is ignored.
- No database, no lock, no middleware: the deduplication comes from the promise ID.

## File structure

```
example-webhook-handler-go/
├── main.go        HTTP server + Resonate workflow + demo runner
├── go.mod         module declaration + SDK pin
├── go.sum         checksums
├── LICENSE        Apache-2.0
└── README.md
```

## Next steps

- [Get started](https://docs.resonatehq.io/get-started) — install paths + first-program walkthrough.
- [Durable execution concepts](https://docs.resonatehq.io/concepts) — how promise IDs become idempotency keys.
- [`example-hello-world-go`](https://github.com/resonatehq-examples/example-hello-world-go) — the simplest possible Resonate program in Go.

## Community

- Discord: <https://resonatehq.io/discord>
- X: <https://x.com/resonatehqio>
- LinkedIn: <https://linkedin.com/company/resonatehq>
- YouTube: <https://youtube.com/@resonatehq>
- Journal: <https://journal.resonatehq.io>

## License

[Apache-2.0](./LICENSE)
