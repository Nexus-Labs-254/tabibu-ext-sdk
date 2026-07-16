# Tutorial: Building the M-Pesa Payments Extension

This tutorial walks through a realistic extension end-to-end. We'll build
**mpesa-payments**: an extension that:

1. Subscribes to the `orders.completed` broker event.
2. Looks up the patient on the completed order using `sdk.Patients().Get()`.
3. Initiates an M-Pesa STK Push to the patient's primary phone.
4. Exposes a payment status dashboard as a WebView UI.
5. Is gated behind `billing:read` AND `patients:read` — users without both privileges
   cannot see this extension's capabilities or open its UI.

---

## Why this extension needs to exist outside the core

The core Tabibu codebase ships to dozens of hospitals. Most of them don't use M-Pesa
(or use a different mobile money provider, or accept cash only). Baking this into the
core would:

- Add an M-Pesa dependency to every deployment that doesn't want it.
- Create a maintenance burden in the core for a provider-specific API.
- Force every hospital through the M-Pesa certification process.

As an extension, the hospital that needs it installs it; everyone else doesn't.

---

## Step 1 — Write the manifest

The manifest is the contract between your extension and Tabibu. Tabibu reads it at
install time to create the DB row and at startup to register capabilities.

```toml
# manifest.toml

[extension]
name        = "mpesa-payments"
version     = "1.0.0"
description = "M-Pesa STK Push payment integration for patient billing"
author      = "Acme Healthcare Tech <dev@acme.co.ke>"
category    = "billing"
min_tabibu  = "1.0.0"

# Both privileges must be held. A receptionist with only billing:read cannot
# open this extension's UI or see its contributed actions.
[extension.privileges]
required = ["billing:read", "patients:read"]

[extension.ui]
has_ui   = true    # extension serves a WebView-embeddable UI
dev_port = 5174    # Vite dev server port (only used when EXT_DEV=true)

# Subscribe to the orders.completed event so we can trigger payment
# automatically when a bill is finalised.
[extension.events]
subscribe = ["orders.completed"]

[extension.config]
mpesa_consumer_key    = ""          # set by admin in Tabibu panel
mpesa_consumer_secret = ""
mpesa_shortcode       = "174379"    # Daraja sandbox default
mpesa_passkey         = ""
mpesa_env             = "sandbox"   # "sandbox" | "production"
payment_timeout_secs  = "30"

[runtime]
binary           = "mpesa-payments"
stop_grace_period = 30

[[contributes.actions]]
id      = "mpesa.initiate"
label   = "Pay via M-Pesa"
context = "billing"          # shown on billing-context screens in the Tabibu app
```

### Key manifest decisions

**`required_privileges = ["billing:read", "patients:read"]`**
Both are required because the extension reads patient demographics (phone number) AND
billing data (amount to charge). A user who only has billing:read but not patients:read
cannot receive the extension's JWT and cannot open its UI. This is enforced server-side
in `ExtToken` — there is no client-side bypass.

**`subscribe = ["orders.completed"]`**
Tabibu's broker delivers a message on this topic whenever a billing order transitions
to `completed`. The extension receives it via `OnEvent` without polling.

**`contributes.actions = [{id: "mpesa.initiate", context: "billing"}]`**
The Tabibu frontend calls `GET /v1/capabilities` after login. If the signed-in user
holds both `billing:read` and `patients:read`, the registry returns this action in the
`billing` context — the frontend can then surface a "Pay via M-Pesa" button on the
billing screen.

---

## Step 2 — Implement the extension

```go
// main.go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "sync"

    sdk "github.com/tabibumrs/tabibu-ext-sdk"
)

func main() {
    if err := sdk.Run(&MPesaExtension{}); err != nil {
        log.Fatal(err)
    }
}

type MPesaExtension struct {
    mu  sync.RWMutex
    cfg mpesaConfig
}

type mpesaConfig struct {
    ConsumerKey    string
    ConsumerSecret string
    Shortcode      string
    Passkey        string
    Env            string
    TimeoutSecs    int
}

// OnStart registers HTTP routes for the payment dashboard UI backend.
func (e *MPesaExtension) OnStart(_ context.Context, server sdk.Server) error {
    server.Get("/health", e.health)
    server.Get("/payments", e.listPayments)
    server.Post("/payments/initiate", e.initiatePayment)
    return nil
}

// OnEvent handles broker events. We only subscribed to orders.completed.
func (e *MPesaExtension) OnEvent(ctx context.Context, event sdk.Event) error {
    if event.Name != "orders.completed" {
        return nil
    }

    // Decode the event payload — the shape matches the billing module's
    // OrderCompletedEvent struct that it publishes to the broker.
    var payload struct {
        OrderID   string `json:"order_id"`
        PatientID string `json:"patient_id"`
        Amount    int64  `json:"amount_cents"`
    }
    if err := json.Unmarshal(event.Payload, &payload); err != nil {
        sdk.Log.Error("orders.completed: bad payload", map[string]any{"err": err})
        return nil // don't return error — a bad payload is not retryable
    }

    // Look up the patient via the IPC service call.
    // This does NOT make an HTTP request to Tabibu — it sends a service_req
    // message over stdin/stdout and waits for the service_res.
    patient, err := sdk.Patients().Get(ctx, payload.PatientID)
    if err != nil {
        sdk.Log.Error("orders.completed: patient lookup failed",
            map[string]any{"patient_id": payload.PatientID, "err": err})
        return err // return error so the broker can retry this message
    }

    if patient.Person.PrimaryPhone == nil {
        sdk.Log.Warn("orders.completed: patient has no phone, skipping STK push",
            map[string]any{"patient_id": payload.PatientID})
        return nil
    }

    phone := *patient.Person.PrimaryPhone
    amount := payload.Amount / 100 // convert cents to KES

    e.mu.RLock()
    cfg := e.cfg
    e.mu.RUnlock()

    if err := initiateStkPush(ctx, cfg, phone, amount, payload.OrderID); err != nil {
        sdk.Log.Error("STK push failed",
            map[string]any{"order_id": payload.OrderID, "phone": phone, "err": err})
        // Don't return the error here — a payment failure shouldn't cause Tabibu
        // to retry the entire event. Record it locally and surface it in the UI.
        recordFailedPayment(payload.OrderID, err.Error())
        return nil
    }

    sdk.Log.Info("STK push initiated",
        map[string]any{"order_id": payload.OrderID, "phone": phone, "amount": amount})
    return nil
}

// OnShutdown is called before the process exits. Finish any in-flight requests.
func (e *MPesaExtension) OnShutdown(_ context.Context) error {
    sdk.Log.Info("mpesa-payments shutting down", nil)
    // In production: flush any pending payment status updates to your local DB.
    return nil
}

// OnConfigUpdate applies new config values without a restart.
// Tabibu calls this when an admin saves new credentials in the panel.
func (e *MPesaExtension) OnConfigUpdate(_ context.Context, cfg sdk.Config) error {
    timeout := 30
    if v, ok := cfg["payment_timeout_secs"]; ok {
        fmt.Sscanf(v, "%d", &timeout)
    }
    e.mu.Lock()
    e.cfg = mpesaConfig{
        ConsumerKey:    cfg["mpesa_consumer_key"],
        ConsumerSecret: cfg["mpesa_consumer_secret"],
        Shortcode:      cfg["mpesa_shortcode"],
        Passkey:        cfg["mpesa_passkey"],
        Env:            cfg["mpesa_env"],
        TimeoutSecs:    timeout,
    }
    e.mu.Unlock()
    sdk.Log.Info("config updated", map[string]any{"env": e.cfg.Env})
    return nil
}

// --- HTTP handlers ---

func (e *MPesaExtension) health(c sdk.Ctx) error {
    return c.JSON(map[string]string{"status": "ok"})
}

func (e *MPesaExtension) listPayments(c sdk.Ctx) error {
    // In a real implementation: read from a local SQLite DB in EXT_DATA_DIR.
    return c.JSON(map[string]any{"payments": []any{}})
}

type initiateRequest struct {
    OrderID string `json:"order_id"`
    Phone   string `json:"phone"`
    Amount  int64  `json:"amount"`
}

func (e *MPesaExtension) initiatePayment(c sdk.Ctx) error {
    var req initiateRequest
    if err := c.BindJSON(&req); err != nil || req.OrderID == "" {
        return c.Status(http.StatusBadRequest).JSON(map[string]string{"error": "invalid request"})
    }

    e.mu.RLock()
    cfg := e.cfg
    e.mu.RUnlock()

    if err := initiateStkPush(c.Context(), cfg, req.Phone, req.Amount, req.OrderID); err != nil {
        return c.Status(http.StatusBadGateway).JSON(map[string]string{"error": err.Error()})
    }
    return c.JSON(map[string]string{"status": "initiated"})
}

// initiateStkPush is a stub — replace with the real Daraja API call.
func initiateStkPush(_ context.Context, cfg mpesaConfig, phone string, amount int64, ref string) error {
    sdk.Log.Info("STK push (stub)",
        map[string]any{"phone": phone, "amount": amount, "ref": ref, "env": cfg.Env})
    return nil
}

func recordFailedPayment(orderID, reason string) {
    sdk.Log.Warn("payment failed", map[string]any{"order_id": orderID, "reason": reason})
}
```

---

## Step 3 — Using the escape-hatch HTTPClient

`sdk.Patients()` covers common patient operations over IPC. For anything else — reading
billing records, checking order status, calling a Tabibu endpoint not exposed via IPC —
use `sdk.HTTPClient()`. It holds a JWT that was exchanged from the extension's API key
at startup and refreshes itself automatically before expiry.

```go
// Example: fetch the full billing record for an order.
func fetchBillingRecord(ctx context.Context, orderID string) (*BillingRecord, error) {
    resp, err := sdk.HTTPClient().Get(ctx, "/v1/billing/orders/"+orderID)
    if err != nil {
        return nil, fmt.Errorf("billing fetch: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusNotFound {
        return nil, fmt.Errorf("order %s not found", orderID)
    }
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("billing fetch: unexpected status %d", resp.StatusCode)
    }

    var record BillingRecord
    if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
        return nil, fmt.Errorf("billing fetch: decode: %w", err)
    }
    return &record, nil
}
```

`sdk.HTTPClient()` sends `Authorization: Bearer <extension-jwt>` on every request.
The JWT is issued by `POST /v1/api/extensions/:name/token` (API key auth) and has
a 1-hour TTL; `keepAlive` refreshes it at the 80% mark automatically.

---

## Step 4 — Build and package

```bash
# Build for the target platform.
GOOS=linux GOARCH=amd64 go build -o bin/mpesa-payments-linux-amd64 .

# Create a canonical symlink the supervisor will use.
ln -sf mpesa-payments-linux-amd64 bin/mpesa-payments

# Bundle into a .tabibu package (zip with manifest.toml at the root).
zip -r mpesa-payments-1.0.0.tabibu manifest.toml bin/ ui/dist/
```

The `.tabibu` archive must contain:
- `manifest.toml` at the zip root.
- `bin/<name>-<goos>-<goarch>` — the platform binary.
- `bin/<name>` — a symlink to the current platform binary (the supervisor uses this).
- `ui/dist/` — the built frontend SPA (only if `extension.ui.has_ui = true`).

---

## Step 5 — Install and run

```bash
# Via CLI (from the server machine).
tabibu extension install ./mpesa-payments-1.0.0.tabibu

# Or via API (remote).
curl -X POST https://hospital.com/v1/api/extensions \
  -H "Authorization: Bearer sk_..." \
  -H "Content-Type: application/json" \
  -d '{"source": "./mpesa-payments-1.0.0.tabibu"}'
```

After install, Tabibu starts the extension automatically. The supervisor sets
`EXT_HTTP_PORT` to an allocated port, `EXT_DATA_DIR` to the extension's data directory,
and writes the extension's API key to `EXT_DATA_DIR/.api_key`.

---

## Development workflow

Use `dev_paths` in `tabibu.toml` to mount a local directory without packaging:

```toml
# tabibu.toml — development only
[extension]
dev_paths = ["/Users/you/mpesa-payments"]
```

Tabibu reads `manifest.toml` from that directory, starts the binary found at
`bin/mpesa-payments`, and sets `EXT_DEV=true`. The extension's UI Vite dev server
runs on the port declared in `manifest.toml`'s `extension.ui.dev_port`, and Tabibu
proxies `/v1/ui/mpesa-payments/*` there instead of to the compiled `ui/dist/`.

Changes to Go code: rebuild and `tabibu extension reload mpesa-payments`.
Changes to UI: Vite hot-reloads automatically.
