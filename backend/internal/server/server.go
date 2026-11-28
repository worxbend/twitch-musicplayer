package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"lofi-radio-backend/internal/api"
	"lofi-radio-backend/internal/player"
	"lofi-radio-backend/internal/websocket"
)

// Options holds all dependencies for the HTTP server.
type Options struct {
	Port         int
	Hub          *websocket.Hub
	Songs        *api.SongsHandler
	Queue        *api.QueueHandler
	Player       *api.PlayerHandler
	BaseURL      string
	QueueMgr     websocket.QueueManager
	StateTracker *player.StateTracker
	// AllowedOrigins are the browser origins allowed to open /ws, read API
	// responses and send writes. BaseURL's own origin is always allowed.
	AllowedOrigins []string
}

// New wires up the Chi router and returns a configured *http.Server.
func New(opts Options) *http.Server {
	return &http.Server{
		Addr:        fmt.Sprintf(":%d", opts.Port),
		Handler:     buildRoutes(opts),
		IdleTimeout: time.Minute,
		ReadTimeout: 10 * time.Second,
		// WriteTimeout guards JSON/API responses. Song streaming lifts it per
		// response in api.SongsHandler.StreamContent.
		WriteTimeout: 30 * time.Second,
	}
}

// logExcept wraps middleware.Logger but skips the given exact paths. The
// Docker healthcheck hits /health every 15s, and logging each probe buries
// real request and websocket logs on a station that runs 24/7.
func logExcept(paths ...string) func(http.Handler) http.Handler {
	skip := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		skip[p] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		logged := middleware.Logger(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := skip[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			logged.ServeHTTP(w, r)
		})
	}
}

// noSniff stops browsers from guessing a content type, so a response such as
// a JSON error or the plain-text now-playing feed is never run as script or
// rendered as HTML.
func noSniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func buildRoutes(opts Options) http.Handler {
	allowOrigin := newOriginPolicy(opts.AllowedOrigins, opts.BaseURL)
	r := chi.NewRouter()
	// First, so 404/405, CORS preflights, foreign-write 403s and recovered
	// panics carry the header too.
	r.Use(noSniff)
	r.Use(logExcept("/health"))
	r.Use(middleware.Recoverer)
	// No credentials: no endpoint uses cookies or HTTP auth.
	r.Use(cors.Handler(cors.Options{
		AllowOriginFunc: func(_ *http.Request, origin string) bool { return allowOrigin(origin) },
		AllowedMethods:  []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:  []string{"Accept", "Authorization", "Content-Type"},
		MaxAge:          300,
	}))
	r.Use(rejectForeignWrites(allowOrigin))
	// GetHead routes HEAD to the GET handler of any path without its own
	// HEAD route; net/http drops the body, so clients get headers only.
	r.Use(middleware.GetHead)

	// HEAD is registered alongside GET because probes commonly use it —
	// `wget --spider` and most load balancers send HEAD, and chi would
	// otherwise answer 405 and report a healthy service as down.
	health := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "up"}) //nolint:errcheck
	}
	r.Get("/health", health)
	r.Head("/health", health)

	r.Method(http.MethodGet, "/ws", &websocket.Endpoint{
		Hub:          opts.Hub,
		QueueMgr:     opts.QueueMgr,
		BaseURL:      opts.BaseURL,
		StateTracker: opts.StateTracker,
		AllowOrigin:  allowOrigin,
	})

	r.Get("/swagger", api.ServeSwagger)
	r.Get("/openapi.yaml", api.ServeOpenAPISpec)

	api.RegisterRoutes(r, opts.Songs, opts.Queue, opts.Player)

	return r
}
