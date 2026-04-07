package server

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/rundb"
)

// Config holds server configuration.
type Config struct {
	Addr string // listen address, e.g. ":8080"
}

// Server is the HTTP server for managing Attractor pipelines.
type Server struct {
	config   Config
	registry *PipelineRegistry
	baseCtx  context.Context
	cancel   context.CancelFunc
	httpSrv  *http.Server
	logger   *log.Logger
}

// New creates a new Server with the given config.
func New(cfg Config) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		config:   cfg,
		registry: NewPipelineRegistry(),
		baseCtx:  ctx,
		cancel:   cancel,
		logger:   log.New(os.Stderr, "[kilroy-server] ", log.LstdFlags),
	}

	mux := http.NewServeMux()

	// Go 1.22+ method+pattern routing.
	mux.HandleFunc("GET /health", s.handleHealth)

	// /runs endpoints (canonical names per platform-reframe plan).
	mux.HandleFunc("POST /runs", s.handleSubmitPipeline)
	mux.HandleFunc("GET /runs", s.handleListRuns)
	mux.HandleFunc("GET /runs/{id}", s.handleGetPipeline)
	mux.HandleFunc("GET /workflows", s.handleListWorkflows)
	mux.HandleFunc("GET /runs/{id}/events", s.handlePipelineEvents)
	mux.HandleFunc("POST /runs/{id}/cancel", s.handleCancelPipeline)
	mux.HandleFunc("GET /runs/{id}/context", s.handleGetContext)
	mux.HandleFunc("GET /runs/{id}/outputs", s.handleGetRunOutputs)
	mux.HandleFunc("GET /runs/{id}/outputs/{name...}", s.handleDownloadOutput)
	mux.HandleFunc("GET /runs/{id}/nodes/{nodeId}/turns", s.handleGetNodeTurns)
	mux.HandleFunc("GET /runs/{id}/nodes/{nodeId}/diff", s.handleGetNodeDiff)
	mux.HandleFunc("GET /runs/{id}/files/{path...}", s.handleBrowseFiles)
	mux.HandleFunc("GET /runs/{id}/workspace/{path...}", s.handleBrowseWorkspace)
	mux.HandleFunc("GET /runs/{id}/questions", s.handleGetQuestions)
	mux.HandleFunc("POST /runs/{id}/questions/{qid}/answer", s.handleAnswerQuestion)

	// /pipelines aliases for backward compatibility.
	mux.HandleFunc("POST /pipelines", s.handleSubmitPipeline)
	mux.HandleFunc("GET /pipelines/{id}", s.handleGetPipeline)
	mux.HandleFunc("GET /pipelines/{id}/events", s.handlePipelineEvents)
	mux.HandleFunc("POST /pipelines/{id}/cancel", s.handleCancelPipeline)
	mux.HandleFunc("GET /pipelines/{id}/context", s.handleGetContext)
	mux.HandleFunc("GET /pipelines/{id}/questions", s.handleGetQuestions)
	mux.HandleFunc("POST /pipelines/{id}/questions/{qid}/answer", s.handleAnswerQuestion)

	s.httpSrv = &http.Server{
		Handler:      csrfProtect(mux, cfg.Addr),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE requires no write timeout
		IdleTimeout:  120 * time.Second,
		BaseContext:  func(net.Listener) context.Context { return ctx },
	}

	return s
}

// ListenAndServe starts the server and blocks until shutdown.
func (s *Server) ListenAndServe() error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		s.logger.Printf("received %s, shutting down...", sig)
		s.Shutdown()
	}()

	// Reconcile stale runs on startup.
	if db, err := rundb.Open(rundb.DefaultPath()); err == nil {
		if n, err := db.ReconcileStaleRuns(2 * time.Hour); err == nil && n > 0 {
			s.logger.Printf("reconciled %d stale run(s) → interrupted", n)
		}
		db.Close()
	}

	s.logger.Printf("listening on %s", s.config.Addr)
	s.httpSrv.Addr = s.config.Addr
	err := s.httpSrv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// csrfProtect rejects cross-origin POST requests. Browsers automatically set
// the Origin header on cross-origin requests, so checking it blocks CSRF from
// malicious web pages while allowing CLI/programmatic callers (which either
// omit Origin or set it to match the server).
func csrfProtect(next http.Handler, _ string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, err := url.Parse(origin)
				if err != nil {
					http.Error(w, `{"error":"invalid Origin header"}`, http.StatusForbidden)
					return
				}
				// Allow only localhost-family origins. This blocks browser-based
				// CSRF from remote pages while allowing local web UIs.
				host := u.Hostname()
				if host != "localhost" && host != "127.0.0.1" && host != "::1" {
					http.Error(w, `{"error":"cross-origin request blocked"}`, http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Shutdown gracefully stops the server and all running pipelines.
func (s *Server) Shutdown() {
	// Cancel all running pipelines.
	s.registry.CancelAll("server shutting down")

	// Give HTTP connections time to drain.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = s.httpSrv.Shutdown(shutdownCtx)

	// Cancel the base context.
	s.cancel()
}
