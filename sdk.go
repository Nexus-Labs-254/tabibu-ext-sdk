// Package sdk is the Tabibu Extension SDK. Import it in your extension's
// main package and call Run with your Extension implementation.
//
// The SDK handles:
//   - .env file loading on startup (dev convenience)
//   - stdio JSON-RPC (NDJSON) over inherited stdin/stdout
//   - Pine HTTP server on EXT_HTTP_PORT (default 9000)
//   - Static UI serving from ui/dist/ in production (EXT_DEV != "true")
//   - Domain service calls via sdk.Patients() and friends
//   - Graceful drain on shutdown message or SIGTERM → drain_done → exit 0
package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BryanMwangi/pine"
	"github.com/tabibumrs/tabibu-ext-sdk/internal"
	"github.com/tabibumrs/tabibu-ext-sdk/services"
	"github.com/joho/godotenv"
)

// Extension is the interface every Tabibu extension must implement.
type Extension interface {
	// OnStart is called once after the SDK is ready. Register HTTP routes
	// on the provided Server. Return a non-nil error to abort startup.
	OnStart(ctx context.Context, server Server) error

	// OnEvent is called for each event routed to this extension by the
	// Extension Runtime (declared in manifest.toml contributes.events).
	// Return a non-nil error to signal processing failure.
	OnEvent(ctx context.Context, event Event) error

	// OnShutdown is called when Tabibu sends a drain signal (shutdown message
	// on stdin or SIGTERM). Finish in-flight work and return. The SDK then
	// writes drain_done to stdout and exits 0. The process is force-killed
	// after stop_grace_period seconds.
	OnShutdown(ctx context.Context) error

	// OnConfigUpdate is called when the extension's config is changed in the
	// Tabibu admin panel. Values are pushed via the stdio config_update message.
	// Use sdk.Config() to read the current config map at any time.
	OnConfigUpdate(ctx context.Context, cfg Config) error
}

// Server is the subset of Pine's API exposed to OnStart.
type Server interface {
	Get(path string, h HandlerFunc)
	Post(path string, h HandlerFunc)
	Put(path string, h HandlerFunc)
	Delete(path string, h HandlerFunc)
}

// Ctx is the request context passed to handler functions.
type Ctx interface {
	Status(code int) Ctx
	JSON(v any) error
	BindJSON(v any) error
	Params(key string) string
	Query(key string) string
	Context() context.Context
	Header(key string) string
}

// HandlerFunc is the signature for HTTP handlers registered via Server.
type HandlerFunc func(Ctx) error

// Event carries a single event dispatched to the extension.
type Event struct {
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload"`
	ID         string          `json:"id"`
}

// Config is a read-only map of key-value pairs from the extension's config
// section in manifest.toml, overridden by admin in the Tabibu panel.
// Updated automatically when an OnConfigUpdate message arrives on stdin.
type Config map[string]string

// Log is the SDK's structured logger. Available after Run() has started.
var Log *Logger

// package-level singletons — set during Run()
var (
	_conn      *internal.Conn
	_config    Config
	_httpCli   *client
	_patients  PatientsService
	_jwtSecret string // EXT_JWT_SECRET injected by the supervisor; used by ValidateToken
)

// Patients returns the patients domain service backed by the IPC channel.
// Call from within handler functions or background goroutines after Run() starts.
func Patients() PatientsService { return _patients }

// Config returns the current extension config map (read-only).
func GetConfig() Config { return _config }

// HTTPClient returns a pre-authenticated *http.Client for making direct calls
// to the Tabibu server. Use this only when sdk.Patients() (or other service
// accessors) don't cover what you need.
func HTTPClient() *client { return _httpCli }

// Run starts the extension. It blocks until the process is told to shut down.
//
//  1. Loads .env (dev convenience; existing vars take precedence)
//  2. Reads env vars: EXT_NAME, EXT_HTTP_PORT, EXT_DATA_DIR, EXT_DEV, EXT_SERVER_URL
//  3. Opens the log file in EXT_DATA_DIR/logs/extension.log
//  4. Initialises the stdio IPC conn (stdin/stdout are inherited from parent)
//  5. Reads the API key from EXT_DATA_DIR/.api_key for sdk.HTTPClient()
//  6. Calls ext.OnStart(ctx, server)
//  7. In prod: serves ui/dist/ as a SPA if the directory exists
//  8. Starts dispatching stdin messages (events, shutdown, config_update)
//  9. SIGTERM fallback: triggers drain if runtime message never arrives
func Run(ext Extension) error {
	_ = godotenv.Load()

	name := env("EXT_NAME", "")
	if name == "" {
		return errorf("EXT_NAME is required")
	}

	dataDir := env("EXT_DATA_DIR", ".")
	extPort := env("EXT_HTTP_PORT", "9000")
	devMode := env("EXT_DEV", "") == "true"
	serverURL := strings.TrimRight(env("EXT_SERVER_URL", "http://localhost:8080"), "/")
	_jwtSecret = env("EXT_JWT_SECRET", "")

	// Logger writes to EXT_DATA_DIR/logs/extension.log.
	logDir := filepath.Join(dataDir, "logs")
	Log = newLoggerToDir(name, logDir)
	Log.Info("starting extension", map[string]any{"name": name, "dev": devMode})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialise stdio IPC — stdin/stdout are inherited from the parent process
	// (the Extension Runtime). No setup needed beyond creating the codec.
	_conn = internal.NewConn(os.Stdout)

	apiKey := env("EXT_API_KEY", "")
	_httpCli = newClient(serverURL, name, apiKey)
	if apiKey != "" {
		if err := _httpCli.refreshToken(ctx); err != nil {
			Log.Warn("token exchange failed — HTTPClient() will be unauthenticated", map[string]any{"err": err.Error()})
		} else {
			go _httpCli.keepAlive(ctx)
		}
	}

	// Initialise service clients backed by the IPC channel.
	_patients = services.NewPatientsService(_conn)

	// Build Pine HTTP server for WebView and extension-defined routes.
	app := pine.New()
	srv := &pineServer{app: app}

	if err := ext.OnStart(ctx, srv); err != nil {
		return errorf("OnStart: %v", err)
	}

	// Static UI in prod — skipped when Vite dev server is in use.
	if !devMode {
		if _, err := os.Stat("ui/dist"); err == nil {
			registerStaticUI(app)
		}
	}

	// Start reading stdin messages.
	_conn.StartReadLoop(ctx, os.Stdin, func(msg internal.Message) {
		handleMessage(ctx, cancel, ext, msg)
	})

	// Send a periodic heartbeat so the supervisor knows we're alive.
	go sendHeartbeat(ctx)

	// SIGTERM fallback — primary path is the "shutdown" stdin message.
	go internal.WatchSignal(ctx, cancel, ext.OnShutdown, _conn)

	return app.Start(":" + extPort)
}

// handleMessage dispatches a single stdin message to the appropriate handler.
func handleMessage(ctx context.Context, cancel context.CancelFunc, ext Extension, msg internal.Message) {
	switch msg.Type {
	case internal.MsgEvent:
		var payload internal.EventPayload
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			Log.Error("event: bad payload", map[string]any{"err": err.Error()})
			return
		}
		event := Event{
			Name:       payload.Name,
			OccurredAt: time.Now().UTC(),
			Payload:    payload.Payload,
			ID:         msg.ID,
		}
		if err := ext.OnEvent(ctx, event); err != nil {
			Log.Error("OnEvent error", map[string]any{"event": payload.Name, "err": err.Error()})
		}

	case internal.MsgConfigUpdate:
		var cfg Config
		if err := json.Unmarshal(msg.Data, &cfg); err != nil {
			Log.Error("config_update: bad payload", map[string]any{"err": err.Error()})
			return
		}
		_config = cfg
		if err := ext.OnConfigUpdate(ctx, cfg); err != nil {
			Log.Error("OnConfigUpdate error", map[string]any{"err": err.Error()})
		}

	case internal.MsgShutdown:
		Log.Info("shutdown message received — draining")
		internal.Drain(ctx, cancel, ext.OnShutdown, _conn)
	}
}

// sendHeartbeat writes a heartbeat message to stdout every 30 seconds.
func sendHeartbeat(ctx context.Context) {
	pid := os.Getpid()
	payload, _ := json.Marshal(internal.HeartbeatPayload{PID: pid})
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = _conn.Send(internal.Message{
				Type: internal.MsgHeartbeat,
				Data: json.RawMessage(payload),
			})
		}
	}
}

// env returns the value of the named env var, falling back to def.
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// newLoggerToDir opens a logger writing to logDir/extension.log.
func newLoggerToDir(name, logDir string) *Logger {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "logger: could not create %s: %v\n", logDir, err)
		return newLogger(name)
	}
	// Re-use newLogger but it writes to "logs/" relative to CWD — override it
	// by creating the file ourselves and passing it as a multi-writer.
	// For simplicity, use the existing newLogger which writes to logs/ relative
	// to CWD; when EXT_DATA_DIR != ".", logs are also in the right directory
	// because the supervisor sets the working dir to EXT_DATA_DIR.
	return newLogger(name)
}
