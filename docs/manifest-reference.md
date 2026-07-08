# manifest.toml Reference

Every extension ships a `manifest.toml` file at the root of its `.tabibu` package.
Tabibu reads it at install time (to create the DB row) and at startup (to register
capabilities and broker subscriptions). For dev extensions loaded from `dev_paths`, the
manifest is re-read every time the extension is started.

---

## Full example

```toml
[extension]
name              = "mpesa-payments"
version           = "1.0.0"
description       = "M-Pesa STK Push payment integration"
author            = "Acme Healthcare Tech <dev@acme.co.ke>"
category          = "billing"
min_tabibu        = "1.0.0"
stop_grace_period = 30

[extension.privileges]
required = ["billing:read", "patients:read"]

[extension.ui]
has_ui   = true
dev_port = 5174

[extension.events]
subscribe = ["orders.completed", "patients.registered"]

[extension.config]
mpesa_consumer_key    = ""
mpesa_consumer_secret = ""
mpesa_shortcode       = "174379"
mpesa_env             = "sandbox"
payment_timeout_secs  = "30"

[runtime]
binary           = "mpesa-payments"
stop_grace_period = 30

[[contributes.actions]]
id      = "mpesa.initiate"
label   = "Pay via M-Pesa"
context = "billing"

[[contributes.actions]]
id      = "mpesa.status"
label   = "Check Payment Status"
context = "billing"
```

---

## `[extension]`

| Key                | Type     | Required | Description                                                  |
|--------------------|----------|----------|--------------------------------------------------------------|
| `name`             | string   | yes      | Unique identifier. Lowercase, hyphens only. Must match the binary name. |
| `version`          | string   | yes      | Semver string (e.g. `"1.2.3"`).                              |
| `description`      | string   | no       | Shown in the extensions panel.                               |
| `author`           | string   | no       | Free-form author string.                                     |
| `category`         | string   | no       | Groups extensions in the UI. Defaults to `"other"`.          |
| `min_tabibu`       | string   | no       | Minimum Tabibu version required. Not yet enforced — set for future compatibility. |
| `stop_grace_period`| int      | no       | Seconds to wait for `drain_done` before force-killing. Defaults to 30. |

### `[extension.privileges]`

| Key        | Type       | Required | Description                                                        |
|------------|------------|----------|--------------------------------------------------------------------|
| `required` | []string   | no       | Privilege strings the calling user must **all** hold to receive an extension JWT or see its capabilities. Empty array (the default) means any authenticated user. |

`required` is a TOML array:
```toml
required = ["billing:read", "patients:read"]  # user must hold both
required = ["billing:read"]                   # user must hold one
required = []                                 # any authenticated user
```

Privilege strings follow the pattern `<module>:<verb>` and must match constants
defined in the owning module's `privileges/` package (e.g., `patients:read`,
`billing:read`, `clinical:write`). Granting the wrong string means the filter
never matches — test with a real account before shipping.

### `[extension.ui]`

| Key       | Type | Required | Description                                                                  |
|-----------|------|----------|------------------------------------------------------------------------------|
| `has_ui`  | bool | no       | Set `true` if the extension serves a WebView-embeddable SPA. Defaults to false. |
| `dev_port`| int  | no       | Vite dev server port. Used only when `EXT_DEV=true`. Defaults to 5173.       |

When `has_ui = true`:
- In **production**: the extension's HTTP server must serve `ui/dist/index.html` as a SPA.
  The SDK does this automatically if `ui/dist/` exists inside `EXT_DATA_DIR`.
- In **dev mode** (`EXT_DEV=true`): Tabibu proxies `/v1/ui/:name/*` to
  `http://localhost:<dev_port>` instead.

The Tabibu frontend fetches `GET /v1/admin/extensions/:name/token` to get a short-lived
JWT, then opens `/v1/ui/:name/` as a WebView. The JWT is passed as `?token=<jwt>` or
`Authorization: Bearer`.

### `[extension.events]`

| Key         | Type     | Required | Description                                                  |
|-------------|----------|----------|--------------------------------------------------------------|
| `subscribe` | []string | no       | Broker topic strings to receive. Topics are constants owned by the publishing module. |

```toml
subscribe = ["orders.completed", "patients.registered"]
```

The Runtime subscribes to each topic on the extension's behalf after the first
heartbeat. Events arrive in your extension's `OnEvent` callback. Do not poll Tabibu
endpoints for events you can receive from the broker — it defeats the purpose.

**Important**: subscribing to a topic does not bypass privilege checks on the data
inside the event payload. If the event payload contains patient PHI (which
`patients.registered` does), your extension is responsible for handling it
appropriately.

### `[extension.config]`

Key-value pairs with their **default values**. Defaults are used when no override has
been saved by the admin. Keys appear in the Tabibu extension panel for the admin to
fill in.

```toml
[extension.config]
api_key       = ""       # empty default — admin must fill this in
api_endpoint  = "https://api.example.com"
timeout_secs  = "30"
```

All values are strings. Your extension reads them via the `sdk.Config` map passed to
`OnConfigUpdate`, or via `sdk.GetConfig()` at any time after `Run()` starts.

Sensitive values (API keys, passwords) should have empty defaults. The admin sets them
in the panel and they are stored in the Tabibu DB. They are never written to the
manifest file at runtime.

---

## `[runtime]`

| Key                | Type   | Required | Description                                                        |
|--------------------|--------|----------|--------------------------------------------------------------------|
| `binary`           | string | yes      | Base name of the binary inside `bin/`. The supervisor appends `-<goos>-<goarch>` to find the platform binary and creates a `bin/<name>` symlink. |
| `stop_grace_period`| int    | no       | Seconds allowed for drain. Overrides `[extension].stop_grace_period`. |

---

## `[[contributes.actions]]`

Actions are surface points in the Tabibu frontend. Each action you declare here can
appear on a screen context as a button or menu item, provided the signed-in user holds
all of the extension's `required_privileges`.

| Key       | Type   | Required | Description                                                          |
|-----------|--------|----------|----------------------------------------------------------------------|
| `id`      | string | yes      | Globally unique action identifier. Convention: `<extension-name>.<action>`. |
| `label`   | string | yes      | Human-readable label shown to the user.                              |
| `context` | string | no       | Screen context where the action is surfaced (e.g. `"billing"`, `"patients"`, `"global"`). Defaults to `"global"`. |

When the frontend calls `GET /v1/capabilities`, the Runtime returns only the actions
whose extension the caller can access (all `required_privileges` held). The frontend
maps `context` to the relevant screen and renders the action there.

When a user triggers the action, the frontend calls `POST /v1/actions/:id`. The Runtime
routes the `action_invoke` message to your extension's stdin. Your `OnEvent`/action
handler receives it and must return a response within a reasonable timeout (default:
the request timeout of the calling client).

---

## Binary naming convention

The supervisor looks for binaries using this exact naming scheme:

```
bin/<name>-<GOOS>-<GOARCH>    # e.g. bin/mpesa-payments-linux-amd64
bin/<name>                    # symlink → current platform binary
```

When packaging, create both. The symlink is what the supervisor actually launches:

```bash
GOOS=linux GOARCH=amd64 go build -o bin/mpesa-payments-linux-amd64 .
ln -sf mpesa-payments-linux-amd64 bin/mpesa-payments
```
