package main

import (
	"context"
	"log"
	"net/http"
	"sync"

	sdk "github.com/tabibumrs/tabibu-ext-sdk"
)

func main() {
	if err := sdk.Run(&HelloExtension{greeting: "Hello from Tabibu!"}); err != nil {
		log.Fatal(err)
	}
}

// HelloExtension is a minimal server-side Tabibu extension.
// It exposes two HTTP endpoints and demonstrates config hot-reload.
// No UI, no broker subscription.
type HelloExtension struct {
	mu       sync.RWMutex
	greeting string
}

// OnStart registers the extension's HTTP routes.
// The server argument is a thin wrapper over Pine; all routes are served on EXT_PORT.
func (e *HelloExtension) OnStart(_ context.Context, server sdk.Server) error {
	server.Get("/health", e.health)
	server.Get("/greet", e.greet)
	return nil
}

// OnEvent is called for each broker message. This extension does not subscribe
// to any topics (EXT_SUBSCRIBE_EVENTS is empty), so this method is never invoked.
func (e *HelloExtension) OnEvent(_ context.Context, event sdk.Event) error {
	sdk.Log.Info("event received", map[string]any{"name": event.Name})
	return nil
}

// OnShutdown is called when Tabibu requests a graceful stop (SIGTERM).
// Finish in-flight work here; the SDK will then notify Tabibu and exit 0.
func (e *HelloExtension) OnShutdown(_ context.Context) error {
	sdk.Log.Info("shutting down", nil)
	return nil
}

// OnConfigUpdate applies new config values to running state without a restart.
// Tabibu calls this whenever the admin updates the extension's config.
func (e *HelloExtension) OnConfigUpdate(_ context.Context, cfg sdk.Config) error {
	if v, ok := cfg["greeting"]; ok {
		e.mu.Lock()
		e.greeting = v
		e.mu.Unlock()
		sdk.Log.Info("config updated", map[string]any{"greeting": v})
	}
	return nil
}

// --- Handlers ---

// GET /health — liveness probe.
func (e *HelloExtension) health(c sdk.Ctx) error {
	return c.JSON(map[string]string{"status": "ok"})
}

// GET /greet — returns the current greeting message.
// Try:  curl http://localhost:9000/greet
func (e *HelloExtension) greet(c sdk.Ctx) error {
	name := c.Query("name")
	if name == "" {
		name = "world"
	}

	e.mu.RLock()
	msg := e.greeting
	e.mu.RUnlock()

	sdk.Log.Info("greet called", map[string]any{"name": name})

	return c.Status(http.StatusOK).JSON(map[string]string{
		"message": msg + " " + name,
	})
}
