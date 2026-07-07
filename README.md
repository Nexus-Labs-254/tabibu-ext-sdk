# tabibu-ext-sdk

Go SDK for building Tabibu extensions. Import it in your extension's `main` package, implement the `Extension` interface, and call `sdk.Run` or create a new extension with:

```bash
tabibu extension init my-extension
```

Extensions run as native child processes managed by the Tabibu server — a similar model VS Code uses for its extensions. The SDK handles all IPC and lifecycle details.

---

## How it works

```
Tabibu Server
│
│  stdin  ──────────────────────────→  Extension process
│  (events, actions, config, shutdown)
│
│  stdout ←──────────────────────────  Extension process
│  (service requests, heartbeats, drain_done)
│
│  HTTP   ←──────────────────────────  Extension WebView
│  (EXT_HTTP_PORT, Pine server inside the extension)
```

Each message is a single JSON line (NDJSON). The SDK reads from `os.Stdin` and writes to `os.Stdout` automatically — extension authors write zero protocol code.

---

## Installation

```bash
go get github.com/Nexus-Labs-254/tabibu-ext-sdk
```

---

## Quick start

```go
package main

import (
    "context"
    "log"

    sdk "github.com/Nexus-Labs-254/tabibu-ext-sdk"
)

func main() {
    if err := sdk.Run(&MyExtension{}); err != nil {
        log.Fatal(err)
    }
}

type MyExtension struct{}

func (e *MyExtension) OnStart(ctx context.Context, server sdk.Server) error {
    server.Get("/hello", func(c sdk.Ctx) error {
        return c.JSON(map[string]string{"hello": "world"})
    })
    return nil
}

func (e *MyExtension) OnEvent(ctx context.Context, event sdk.Event) error {
    sdk.Log.Info("event received", map[string]any{"name": event.Name})
    return nil
}

func (e *MyExtension) OnShutdown(ctx context.Context) error {
    return nil // finish in-flight work here
}

func (e *MyExtension) OnConfigUpdate(ctx context.Context, cfg sdk.Config) error {
    // cfg["EXAMPLE_KEY"] — read-only values set in the Tabibu admin panel
    return nil
}
```

---

## Env vars

These are set by the Extension Runtime when it spawns your process. You do **not** set them yourself in production; in dev mode, put them in `.env`:

| Variable         | Description                                                  |
| ---------------- | ------------------------------------------------------------ |
| `EXT_NAME`       | Extension name (matches `manifest.toml`)                     |
| `EXT_HTTP_PORT`  | Port for the Pine HTTP server (WebView and extension routes) |
| `EXT_DATA_DIR`   | Persistent data directory for this extension                 |
| `EXT_DEV`        | `"true"` in dev mode — disables static UI serving            |
| `EXT_SERVER_URL` | Tabibu server URL — used only by `sdk.HTTPClient()`          |

---

## Service layer

Instead of calling Tabibu's HTTP APIs directly, use the service accessors. Calls are routed through the IPC channel to the Extension Runtime, which executes them in-process.

### `sdk.Patients()`

```go
// List patients
patients, err := sdk.Patients().List(ctx, "John")

// Get a single patient
patient, err := sdk.Patients().Get(ctx, "uuid-here")

// Register a new patient
patient, err := sdk.Patients().Register(ctx, sdk.RegisterPatientRequest{
    GivenName:  "Jane",
    FamilyName: "Doe",
    Sex:        "F",
    Phone:      "+254700000000",
})
```

### `sdk.GetConfig()`

Returns the extension's own config map — key/value pairs declared in `manifest.toml [extension.config]` and editable in the Tabibu admin panel. Values are **read-only** from the extension's perspective; updates flow inward via `OnConfigUpdate`.

```go
cfg := sdk.GetConfig()
shortcode := cfg["shortcode"]
```

### `sdk.HTTPClient()`

Returns a `*client` that is pre-authenticated with a JWT (exchanged from the API key written to `$EXT_DATA_DIR/.api_key`). Use this for Tabibu API calls not yet covered by the service layer.

```go
resp, err := sdk.HTTPClient().Get(ctx, "/v1/billing/bills/"+billID)
```

---

## Events

Subscribe to events by declaring them in `manifest.toml`:

```toml
[[contributes.events]]
subscribe = "billing.payment_requested"
```

The Extension Runtime subscribes to the broker on your behalf and delivers matching events to `OnEvent` over stdin. Extensions never hold broker credentials.

```go
func (e *MyExtension) OnEvent(ctx context.Context, event sdk.Event) error {
    switch event.Name {
    case sdk.EventPaymentRequested:
        var payload sdk.PaymentRequestedPayload
        _ = json.Unmarshal(event.Payload, &payload)
        // handle payment...
    }
    return nil
}
```

---

## Dev mode

### Go backend

In dev mode the Extension Runtime spawns your extension using `go run .` in the source directory instead of the compiled binary. This lets tools like `air` manage restarts on file changes.

**No extra configuration is needed.** When you run:

```bash
tabibu server start --dev --ext-dev-dir ./path/to/my-extension
```

the server:

1. Reads `manifest.toml` from the dev path
2. Registers the extension in the DB (idempotent)
3. Spawns `go run .` in the source directory with `EXT_DEV=true`

Reload after a Go change:

```bash
tabibu extension reload my-extension
```

Or install `air` for automatic restarts:

```bash
go install github.com/air-verse/air@latest
# In your extension directory:
air
```

### UI hot reload (Vite)

Declare `dev_port` in `manifest.toml`:

```toml
[extension.ui]
has_ui   = true
dev_port = 5173
```

When `EXT_DEV=true`, the Extension Runtime proxies `GET /v1/ui/<name>/*` to `http://localhost:<dev_port>` instead of the compiled `ui/dist/`. Start Vite separately:

```bash
cd my-extension/ui && npm run dev   # Vite listens on dev_port
```

Changes appear instantly — no reload needed.

In production (no `EXT_DEV`), the Runtime proxies to the Pine HTTP server inside the extension process (`EXT_HTTP_PORT`), which serves `ui/dist/` as a SPA.

---

## manifest.toml reference

```toml
[extension]
name        = "my-extension"
version     = "1.0.0"
description = "Does something useful"
author      = "You <you@example.com>"
category    = "billing"
min_tabibu  = "1.0.0"

[extension.privileges]
required = "billing.view"    # leave empty to allow any authenticated user

[extension.ui]
has_ui   = false
dev_port = 5173              # Vite port — only used when EXT_DEV=true

[runtime]
binary            = "bin/my-extension"  # relative path inside the .tabibu package
stop_grace_period = 30                  # seconds before SIGKILL after shutdown

[[contributes.actions]]
id      = "billing.pay_mpesa"
label   = "Pay via M-Pesa"
context = "billing.bill"

[[contributes.events]]
subscribe = "billing.payment_requested"

[extension.config]
shortcode    = ""   # editable in Tabibu admin panel; read via sdk.GetConfig()
callback_url = ""
```

---

## Production packaging

```bash
# 1. Build and package
tabibu extension build .
# → my-extension-1.0.0.tabibu

# 2. Install on server
tabibu extension install ./my-extension-1.0.0.tabibu

# 3. Or install from configured registry
tabibu extension install my-extension
```

The `.tabibu` archive contains:

```
my-extension-1.0.0.tabibu
    manifest.toml
    bin/
        my-extension-linux-amd64
        my-extension-darwin-arm64
    ui/dist/          (optional, if has_ui = true)
    signature.sig     (optional, Ed25519 over SHA256 of manifest.toml)
```

To sign packages, set `TABIBU_SIGN_KEY` to a base64-encoded Ed25519 private key before running `tabibu extension build`. The server verifies the signature when `extensions.registry_public_key` is configured.

---

## Graceful shutdown

The Extension Runtime sends `{"type":"shutdown","grace_seconds":N}` on stdin. The SDK:

1. Calls `OnShutdown` (30 s timeout)
2. Writes `{"type":"drain_done"}` to stdout
3. Cancels the context and exits 0

If the process doesn't exit within `stop_grace_period` seconds, the Runtime sends SIGTERM then SIGKILL. The `WatchSignal` goroutine inside the SDK handles SIGTERM as a fallback (same drain sequence, same `drain_done` message).
