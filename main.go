// Package main demonstrates exactly-once webhook processing via
// event-ID-as-promise-ID deduplication.
//
// # What this demonstrates
//
// A net/http server receives Stripe-style payment webhooks. The event_id field
// is used directly as the Resonate promise ID. When the same event arrives a
// second time (Stripe retrying after a network timeout), Run finds the existing
// promise and returns the cached result without re-executing the handler body.
// No deduplication table, no Redis lock, no extra state required.
//
// The demo sends the same event twice — with different payloads on the second
// delivery — and shows that the second call returns the result from the first.
//
// # Flags
//
//	-url=<server>   Resonate server URL (default: localnet, no server needed)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	resonate "github.com/resonatehq/resonate-sdk-go"
	"github.com/resonatehq/resonate-sdk-go/localnet"
)

// ─────────────────────────────────────────────────────────────────────────────
// Domain types
// ─────────────────────────────────────────────────────────────────────────────

// WebhookEvent mirrors a minimal Stripe payment_intent.succeeded event.
type WebhookEvent struct {
	EventID    string `json:"event_id"`
	Type       string `json:"type"`
	Amount     int    `json:"amount"`
	Currency   string `json:"currency"`
	CustomerID string `json:"customer_id"`
}

// ChargeResult is the value returned by processPayment and cached in the promise.
type ChargeResult struct {
	EventID    string `json:"event_id"`
	CustomerID string `json:"customer_id"`
	Amount     int    `json:"amount"`
	Currency   string `json:"currency"`
	Status     string `json:"status"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Workflow function — registered with Resonate
// ─────────────────────────────────────────────────────────────────────────────

// processPayment is the durable handler. It runs exactly once per event_id
// regardless of how many times the HTTP endpoint is called with that ID.
func processPayment(_ *resonate.Context, event WebhookEvent) (ChargeResult, error) {
	fmt.Printf("[handler] charging $%d for %s (customer %s)\n",
		event.Amount, event.EventID, event.CustomerID)

	// Simulate calling a payment processor.
	result := ChargeResult{
		EventID:    event.EventID,
		CustomerID: event.CustomerID,
		Amount:     event.Amount,
		Currency:   event.Currency,
		Status:     "charged",
	}
	return result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP server
// ─────────────────────────────────────────────────────────────────────────────

func buildServer(r *resonate.Resonate, processPaymentFn *resonate.RegisteredFunc[WebhookEvent, ChargeResult]) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /webhook", func(w http.ResponseWriter, req *http.Request) {
		var event WebhookEvent
		if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if event.EventID == "" || event.Type == "" {
			http.Error(w, "missing event_id or type", http.StatusBadRequest)
			return
		}

		fmt.Printf("[server]  received %s (amount=$%d) — calling Run\n",
			event.EventID, event.Amount)

		// The event_id IS the promise ID. If this event was already processed,
		// Run returns a handle to the existing promise immediately — no
		// re-execution, no duplicate charge.
		promiseID := "webhook/" + event.EventID
		h, err := processPaymentFn.Run(req.Context(), promiseID, event)
		if err != nil {
			http.Error(w, fmt.Sprintf("run error: %v", err), http.StatusInternalServerError)
			return
		}

		result, err := h.Result(req.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("result error: %v", err), http.StatusInternalServerError)
			return
		}

		fmt.Printf("[server]  returned: charged $%d\n", result.Amount)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})

	// Health check — used by the demo runner to wait for the server to be ready.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	_ = r // kept for potential future use (e.g. promise introspection endpoint)
	return mux
}

// ─────────────────────────────────────────────────────────────────────────────
// Demo runner — sends two deliveries of the same event
// ─────────────────────────────────────────────────────────────────────────────

func runDemo(addr string) {
	// Wait for the server to accept connections.
	baseURL := "http://" + addr
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	eventID := fmt.Sprintf("evt_%d", time.Now().UnixMilli())

	fmt.Println()
	fmt.Println("=== Webhook Handler Demo ===")
	fmt.Println("Mode: DEDUPLICATION (same webhook sent twice, processed once)")
	fmt.Println()

	// ── First delivery ──────────────────────────────────────────────────────
	firstEvent := WebhookEvent{
		EventID:    eventID,
		Type:       "payment_intent.succeeded",
		Amount:     100,
		Currency:   "usd",
		CustomerID: "cus_alice",
	}
	fmt.Printf("--- First delivery of %s (amount=$%d) ---\n", eventID, firstEvent.Amount)
	result1 := postWebhook(baseURL, firstEvent)
	fmt.Printf("[demo]    first result:  charged $%d (status: %s)\n", result1.Amount, result1.Status)

	// ── Second delivery — same event_id, tampered payload ──────────────────
	// Simulates Stripe retrying after a network timeout. The payload carries
	// a different amount to prove that the handler body does NOT re-run.
	secondEvent := WebhookEvent{
		EventID:    eventID, // same ID
		Type:       "payment_intent.succeeded",
		Amount:     999, // different amount — would be a double-charge if re-executed
		Currency:   "usd",
		CustomerID: "cus_alice",
	}
	fmt.Println()
	fmt.Printf("--- Stripe retries %s (simulating network timeout — amount=$%d) ---\n",
		eventID, secondEvent.Amount)
	result2 := postWebhook(baseURL, secondEvent)
	fmt.Printf("[demo]    second result: charged $%d (status: %s)\n", result2.Amount, result2.Status)

	// ── Verdict ─────────────────────────────────────────────────────────────
	fmt.Println()
	if result1.Amount == result2.Amount && result1.Amount == firstEvent.Amount {
		fmt.Println("=== PASS: deduplication worked ===")
		fmt.Printf("Both deliveries returned $%d — the $%d retry payload was ignored.\n",
			result1.Amount, secondEvent.Amount)
		fmt.Println("The handler body ran exactly once. No duplicate charge.")
	} else {
		log.Fatalf("FAIL: expected both results to be $%d, got $%d and $%d",
			firstEvent.Amount, result1.Amount, result2.Amount)
	}
}

func postWebhook(baseURL string, event WebhookEvent) ChargeResult {
	body, err := json.Marshal(event)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(baseURL+"/webhook", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("POST /webhook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("POST /webhook: unexpected status %d", resp.StatusCode)
	}
	var result ChargeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Fatalf("decode response: %v", err)
	}
	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// Main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	serverURL := flag.String("url", "", "Resonate server URL (omit for localnet)")
	flag.Parse()

	// ── Build Resonate instance ──────────────────────────────────────────────
	var cfg resonate.Config
	if *serverURL != "" {
		cfg = resonate.Config{URL: *serverURL}
	} else {
		pid := "webhook-worker"
		cfg = resonate.Config{
			Network:   localnet.NewLocal("default", &pid),
			Heartbeat: resonate.NoopHeartbeat{},
		}
	}

	r, err := resonate.New(cfg)
	if err != nil {
		log.Fatalf("resonate.New: %v", err)
	}
	defer func() { _ = r.Stop() }()

	// ── Register the payment workflow ────────────────────────────────────────
	processPaymentFn, err := resonate.Register(r, "processPayment", processPayment)
	if err != nil {
		log.Fatalf("Register: %v", err)
	}

	// ── Start HTTP server on a random localhost port ─────────────────────────
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("net.Listen: %v", err)
	}
	addr := listener.Addr().String()

	srv := &http.Server{
		Handler:      buildServer(r, processPaymentFn),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Run demo in a goroutine; it calls os.Exit(0) when done.
	go func() {
		runDemo(addr)

		// Clean shutdown.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("server shutdown: %v", err)
		}
	}()

	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}
