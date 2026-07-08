# Extension Security Guide

Extensions run as separate OS processes and operate within Tabibu's privilege and
authentication model. This guide explains the security mechanisms available to you, what
Tabibu enforces automatically, and what you are responsible for yourself.

---

## The two access control layers

### 1. Who can use the extension

`required_privileges` in `manifest.toml` gates who can:

- Receive an extension JWT via `GET /v1/admin/extensions/:name/token`.
- See the extension's contributed capabilities at `GET /v1/capabilities`.
- Open the extension's embedded UI (the UI route returns 401 if the token isn't valid).

All entries in `required_privileges` are checked with AND semantics:
the user must hold **every** listed privilege. A missing privilege means no token,
no capabilities, no UI.

```toml
[extension.privileges]
required = ["billing:read", "patients:read"]
```

Super admins bypass this check — `IsSuperAdmin` in the JWT short-circuits all privilege
gates on the Tabibu server side.

This check is **enforced by Tabibu**, not by your extension. You do not need to verify
the JWT inside your own HTTP handlers for this specific gate — Tabibu's `ExtToken`
handler does it before issuing the token.

### 2. What the extension can access

Service calls (`sdk.Patients().List()`, etc.) go through the Extension Runtime's
`ServiceProxy`. The proxy calls the domain service **as the system user**
(`IsSuperAdmin: true`) — meaning your extension can read and write any patient record,
regardless of restricted-resource ACLs.

This is intentional for background processing (e.g., reacting to a broker event where
you don't have a user context). But it means:

> **Your extension is responsible for not exposing data to users who shouldn't see it.**

If your UI shows patient records fetched via `sdk.Patients()`, you must ensure the
signed-in user would be permitted to see those records under normal Tabibu ACLs. In
practice: gate the data behind the same `required_privileges` you declared, and don't
return patient data to unauthenticated WebView requests.

---

## Authentication paths

There are three authentication paths into your extension:

### Path A — Tabibu reverse-proxied UI (WebView)

`GET /v1/ui/:name/*` in Tabibu validates an extension JWT before forwarding the request
to your HTTP server. Your server receives the already-validated traffic — you don't need
to re-validate the JWT in every handler.

However, if your UI makes secondary API calls back to **your extension's HTTP server
directly** (not through the `/v1/ui/:name/` proxy), those requests bypass Tabibu's
validation. Validate the JWT yourself:

```go
func (e *Extension) requireAuth(c sdk.Ctx) error {
    tok := strings.TrimPrefix(c.Header("Authorization"), "Bearer ")
    if tok == "" {
        return c.Status(http.StatusUnauthorized).JSON(map[string]string{"error": "unauthorized"})
    }
    // The JWT was issued by Tabibu's auth.GenerateExtensionToken.
    // Verify it using the shared JWT secret (EXT_JWT_SECRET, injected by Tabibu).
    // sdk.ValidateToken() is a planned helper — until then, use golang-jwt/jwt directly.
    return nil
}
```

In practice, **route all WebView API calls through the `/v1/ui/:name/` proxy** rather
than hitting your extension port directly — that's what the proxy exists for.

### Path B — Extension API key (server-to-Tabibu)

When your extension starts, the supervisor writes an API key to `EXT_DATA_DIR/.api_key`.
The SDK reads it at startup and exchanges it for a JWT via
`POST /v1/admin/extensions/:name/token` (the extension API key auth path, not the
user JWT path).

The JWT is stored in-memory and used by `sdk.HTTPClient()` for all calls back to
Tabibu. `keepAlive` refreshes it automatically at the 80% mark. **Never log or expose
the API key or JWT.**

```
EXT_DATA_DIR/.api_key   → kept secret, readable only by the extension process
Extension JWT (in-memory) → 1-hour TTL, auto-refreshed
```

### Path C — Broker events

Events arrive over stdin, which is an OS pipe inherited from the Tabibu parent process.
There is no authentication on individual events — Tabibu is the sole writer of stdin.
Treat event payloads as trusted but **not** as authoritative user input; validate the
shape before using the data.

---

## Handling sensitive config values

Config values (M-Pesa API keys, webhook secrets, etc.) are stored in the Tabibu DB
and pushed to your extension via the `config_update` stdio message. They are never
written to `manifest.toml` at runtime.

```toml
# manifest.toml — correct: empty default forces admin to fill it in
[extension.config]
mpesa_consumer_key = ""   # admin sets this; it stays in the DB, not on disk

# WRONG: never put real credentials in manifest.toml
mpesa_consumer_key = "abc123_my_real_key"
```

Inside your extension, store config values only in memory (protected by a mutex):

```go
type myConfig struct {
    ConsumerKey    string
    ConsumerSecret string
}

type MyExtension struct {
    mu  sync.RWMutex
    cfg myConfig
}

func (e *MyExtension) OnConfigUpdate(_ context.Context, cfg sdk.Config) error {
    e.mu.Lock()
    e.cfg = myConfig{
        ConsumerKey:    cfg["mpesa_consumer_key"],
        ConsumerSecret: cfg["mpesa_consumer_secret"],
    }
    e.mu.Unlock()
    return nil
}
```

Never write config values to your extension's local SQLite, log files, or HTTP
responses.

---

## Patient data and PHI obligations

Tabibu is a clinical system. Extension data access is subject to the same data
governance obligations as the core application.

**Rules for handling patient data in your extension:**

1. **Minimise access.** Only request the privileges your extension actually needs. If you
   only need to know the patient's phone number to send an SMS, you don't need to call
   `sdk.Patients().Get()` and cache the full demographic record.

2. **Don't store PHI locally.** Your extension's `EXT_DATA_DIR` is a local file path on
   the hospital's server. Storing patient names, DOBs, or contact details there
   creates a copy of PHI outside Tabibu's audit trail and backup. Store IDs and
   references only; look up demographics at point-of-use.

3. **Don't log PHI.** `sdk.Log` writes to a file inside `EXT_DATA_DIR/logs/`. Logging
   patient names or phone numbers creates a persistent record outside normal audit scope.
   Log `patient_id` (a UUID) and `order_id` only.

4. **Treat all service call results as sensitive.** `sdk.Patients().List()` with an
   empty query returns all patients. Don't call it on a wide sweep unless you have a
   real operational reason. Prefer `sdk.Patients().Get(ctx, id)` with a known ID from
   the event payload.

---

## Verifying the extension JWT in your UI handlers (reference)

The extension JWT (issued by Tabibu for WebView embedding) is a standard HS256 JWT
signed with Tabibu's shared `AUTH_JWT_SECRET`. The claims structure is:

```json
{
  "sub": "mpesa-payments",
  "privileges": ["billing:read", "patients:read"],
  "exp": 1234567890
}
```

If you need to validate it yourself (e.g., for API endpoints on your extension's port
that are NOT proxied through `/v1/ui/:name/`):

```go
import "github.com/golang-jwt/jwt/v5"

type ExtClaims struct {
    Privileges []string `json:"privileges"`
    jwt.RegisteredClaims
}

func parseExtToken(tokenStr, secret string) (*ExtClaims, error) {
    tok, err := jwt.ParseWithClaims(tokenStr, &ExtClaims{},
        func(t *jwt.Token) (any, error) {
            if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
                return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
            }
            return []byte(secret), nil
        })
    if err != nil {
        return nil, err
    }
    if claims, ok := tok.Claims.(*ExtClaims); ok && tok.Valid {
        return claims, nil
    }
    return nil, fmt.Errorf("invalid token")
}
```

Tabibu injects the JWT secret as `EXT_JWT_SECRET` (planned — not yet implemented).
Until that env var is available, share the secret via a secure config value.

---

## Dependency isolation

Each extension runs as a separate OS process. A crashing extension does not crash
Tabibu. A memory leak in your extension does not leak into Tabibu's heap. An extension
that opens a network connection to a compromised external service cannot read Tabibu's
in-memory state.

What extensions CAN do that you should be aware of:
- **Read and write the Tabibu database** via service calls (as system user).
- **Call `sdk.HTTPClient()` to hit any Tabibu API endpoint** the system user can reach.
- **Write to disk** within `EXT_DATA_DIR` (the supervisor owns this directory).
- **Make outbound network calls** — Tabibu does not restrict extension network access.

There is no sandbox preventing an extension from opening arbitrary sockets or reading
arbitrary files from disk. Only install extensions you trust, signed by publishers you
trust.
