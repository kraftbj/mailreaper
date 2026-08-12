package server

import (
	"context"
	"net"
	"net/http"
	"strconv"

	"github.com/kraftbj/mailreaper/internal/db"
)

// Server holds the HTTP mux and its dependencies.
type Server struct {
	db                *db.DB
	mux               *http.ServeMux
	handler           http.Handler // mux wrapped with withSameOrigin; always serve this, never mux directly
	bindAddr          string
	port              int
	OnScanRequested   func()     // called when user clicks "Scan Now"
	OnRescanRequested func() // full rescan: no lookback limit, no message cap
}

// NewServer creates a Server and registers all routes on the mux.
func NewServer(database *db.DB, bindAddr string, port int) *Server {
	s := &Server{
		db:       database,
		mux:      http.NewServeMux(),
		bindAddr: bindAddr,
		port:     port,
	}
	s.registerRoutes()
	s.handler = withSameOrigin(s.mux)
	return s
}

// ServeHTTP implements http.Handler so the server can be used with httptest.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// Start begins listening on the configured port. It blocks until the context
// is cancelled, then gracefully shuts down the server.
func (s *Server) Start(ctx context.Context) error {
	addr := net.JoinHostPort(s.bindAddr, strconv.Itoa(s.port))
	httpSrv := &http.Server{Addr: addr, Handler: s.handler}

	go func() {
		<-ctx.Done()
		httpSrv.Shutdown(context.Background())
	}()

	err := httpSrv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// registerRoutes wires all API endpoints to their handlers.
func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/rules", s.handleGetRules)
	s.mux.HandleFunc("POST /api/rules", s.handleSaveRule)
	s.mux.HandleFunc("DELETE /api/rules/{id}", s.handleDeleteRule)

	s.mux.HandleFunc("GET /api/activity", s.handleGetActivity)

	s.mux.HandleFunc("GET /api/verdicts/pending", s.handleGetPendingVerdicts)
	s.mux.HandleFunc("PUT /api/verdicts/{messageID}/status", s.handleUpdateVerdictStatus)

	s.mux.HandleFunc("GET /api/categories", s.handleGetCategories)
	s.mux.HandleFunc("POST /api/categories", s.handleSaveCategory)
	s.mux.HandleFunc("DELETE /api/categories/{id}", s.handleDeleteCategory)

	s.mux.HandleFunc("GET /api/training", s.handleGetTrainingExamples)
	s.mux.HandleFunc("DELETE /api/training/{id}", s.handleDeleteTrainingExample)

	s.mux.HandleFunc("GET /api/stats", s.handleGetStats)
	s.mux.HandleFunc("POST /api/scan", s.handleScanNow)
	s.mux.HandleFunc("POST /api/rescan", s.handleRescan)

	s.mux.HandleFunc("GET /api/events", s.handleSSE)

	// Static UI files.
	s.mux.Handle("/", http.FileServer(http.Dir("ui")))
}
