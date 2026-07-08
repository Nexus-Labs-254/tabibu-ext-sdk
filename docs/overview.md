# Tabibu Extension System — Overview

## Why extensions exist

Tabibu ships a core clinical workflow: register patients, queue them, triage, consult,
write orders, dispense, and bill. Every hospital that deploys it shares that core.

But hospitals are not identical. A hospital in Nairobi might need M-Pesa payment
integration. A mission hospital in western Kenya might need to synchronise patients with
a national health registry. A specialist clinic might need a custom radiology reading
dashboard. None of those belong in the Tabibu core — they're too specific, too diverse,
and change at a completely different pace from the clinical workflow.

Extensions are the answer: **self-contained features that run alongside Tabibu without
touching its source code.** A hospital can install, update, and remove an extension
without ever restarting their database or rebuilding the main server.

---

## Architecture in one sentence

An extension is a separate OS process — typically a compiled Go binary — that Tabibu
spawns, monitors, and communicates with over a private stdio JSON-RPC channel.

```
  ┌─────────────────────────────────────────────────────┐
  │  Tabibu Server (cmd/server.go)                      │
  │                                                     │
  │  Extension Runtime (pkg/extension/)                 │
  │    Supervisor  ──────────── spawns / watches        │
  │    ServiceProxy ─────────── routes service calls    │
  │    Registry ─────────────── tracks capabilities     │
  │         │          ▲                                │
  │     stdin/stdout   │  (NDJSON, one JSON per line)   │
  └─────────┼──────────┼──────────────────────────────-─┘
            │          │
  ┌─────────▼──────────┴──────────────────────────────-─┐
  │  Your Extension Process                             │
  │                                                     │
  │  sdk.Run(&YourExtension{})                          │
  │    • HTTP server (EXT_HTTP_PORT)                    │
  │    • IPC message loop (stdin/stdout)                │
  │    • Service calls  → sdk.Patients().List(...)      │
  │    • Broker events  ← OnEvent(ctx, event)           │
  │    • Config updates ← OnConfigUpdate(ctx, cfg)      │
  └─────────────────────────────────────────────────────┘
```

### What the extension runtime does

- **Spawns** extension binaries on server startup (and on `POST /reload`).
- **Monitors** them: restarts crashed extensions, tracks PID and allocated port.
- **Routes** `service_req` messages from extension stdout to the relevant domain
  service inside Tabibu (e.g., `sdk.Patients().Get()` → `patients` module).
- **Delivers** broker events to extensions that subscribed in their manifest.
- **Filters** capabilities at `GET /v1/capabilities` based on the calling user's
  privilege set so users only see actions from extensions they can actually access.
- **Issues tokens** at `GET /v1/admin/extensions/:name/token` for WebView embedding.

### What your extension owns

- Its **HTTP server** — routes you register in `OnStart` are served on `EXT_HTTP_PORT`.
  Tabibu reverse-proxies `/v1/ui/:name/*` to that port so the Tabibu frontend can
  embed your UI inside a WebView.
- Its **data directory** (`EXT_DATA_DIR`) — logs, any local SQLite, config files.
- Its **config** — key/value pairs set in `manifest.toml` and overridable by the admin
  in the Tabibu panel without a restart.
- Its **capabilities** — actions and events it declares in `[contributes]` become
  visible to the Tabibu frontend for users who hold the required privileges.

---

## The stdio protocol

Communication between Tabibu and your extension happens over the process's inherited
stdin (Tabibu → extension) and stdout (extension → Tabibu). Each message is a single
JSON object terminated by a newline (NDJSON).

You never write this protocol by hand — the SDK handles it. This section exists so you
understand what's happening under the hood when you call `sdk.Patients().List()` or
receive an `OnEvent` callback.

### Messages Tabibu sends to your extension (on stdin)

| `type`          | When                                                           |
|-----------------|----------------------------------------------------------------|
| `event`         | A broker event you subscribed to fired                         |
| `action_invoke` | A user triggered one of your contributed actions               |
| `config_update` | An admin changed your extension's config in the panel          |
| `shutdown`      | Graceful stop — drain in-flight work, then exit                |

### Messages your extension sends to Tabibu (on stdout)

| `type`        | When                                                           |
|---------------|----------------------------------------------------------------|
| `heartbeat`   | Every 30 s — proves the process is alive                       |
| `service_req` | You called `sdk.Patients().Get()` or similar                   |
| `action_res`  | You finished handling an `action_invoke`                       |
| `drain_done`  | You're done draining after a `shutdown` message                |

---

## Extension lifecycle

```
install  → binary extracted to EXT_DATA_DIR/extensions/<name>/
           DB row created (name, version, required_privileges, …)
           
startup  → supervisor spawns the binary
           extension sends heartbeat
           Runtime registers capabilities + broker subscriptions
           
running  → HTTP requests served on EXT_HTTP_PORT
           service calls routed via IPC
           broker events delivered on stdin
           
reload   → stop + start (config changes, update)

stop     → Runtime sends { type: "shutdown", data: { grace_seconds: N } }
           extension calls OnShutdown, finishes in-flight work
           extension sends { type: "drain_done" }
           process exits 0
           (force-killed after grace_seconds if drain_done never arrives)

remove   → process stopped, DB row soft-deleted, binary directory optionally removed
```

---

## Environment variables injected by Tabibu

| Variable         | Example value                    | Description                              |
|------------------|----------------------------------|------------------------------------------|
| `EXT_NAME`       | `mpesa-payments`                 | The extension's registered name          |
| `EXT_HTTP_PORT`  | `9001`                           | Port your HTTP server must listen on     |
| `EXT_DATA_DIR`   | `/var/tabibu/extensions/mpesa-payments` | Root for logs, data, API key file |
| `EXT_DEV`        | `true`                           | Set in dev mode (Vite hot-reload active) |
| `EXT_SERVER_URL` | `http://localhost:3080`          | Tabibu server URL for HTTPClient()       |

The SDK reads all of these inside `Run()`. You never read them directly.
