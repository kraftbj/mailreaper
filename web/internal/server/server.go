package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kraftbj/mailreaper/internal/db"
)

// Server holds the HTTP mux and its dependencies.
type Server struct {
	db   *db.DB
	mux  *http.ServeMux
	port int
}

// NewServer creates a Server and registers all routes on the mux.
func NewServer(database *db.DB, port int) *Server {
	s := &Server{
		db:   database,
		mux:  http.NewServeMux(),
		port: port,
	}
	s.registerRoutes()
	return s
}

// ServeHTTP implements http.Handler so the server can be used with httptest.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Start begins listening on the configured port. It blocks until the context
// is cancelled, then gracefully shuts down the server.
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf(":%d", s.port)
	httpSrv := &http.Server{Addr: addr, Handler: s.mux}

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

	s.mux.HandleFunc("GET /api/events", s.handleSSE)

	// Static UI files.
	s.mux.Handle("/", http.FileServer(http.Dir("ui")))
}
