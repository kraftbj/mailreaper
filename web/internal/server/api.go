package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

// jsonResponse writes data as JSON with a 200 status. Empty slices serialize
// as [] rather than null.
func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// jsonError writes an error message as JSON.
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleGetRules returns all rules as a JSON array.
func (s *Server) handleGetRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.db.GetRules()
	if err != nil {
		jsonError(w, "failed to get rules", http.StatusInternalServerError)
		return
	}
	if rules == nil {
		rules = []db.Rule{}
	}
	jsonResponse(w, rules)
}

// handleSaveRule decodes a rule from the request body and upserts it.
func (s *Server) handleSaveRule(w http.ResponseWriter, r *http.Request) {
	var rule db.Rule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := s.db.SaveRule(rule); err != nil {
		jsonError(w, "failed to save rule", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, rule)
}

// handleDeleteRule removes a rule by its path parameter ID.
func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteRule(id); err != nil {
		jsonError(w, "failed to delete rule", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetActivity returns recent activity log entries. Accepts ?limit=N (default 50).
func (s *Server) handleGetActivity(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := s.db.GetActivityLog(limit)
	if err != nil {
		jsonError(w, "failed to get activity", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []db.ActivityEntry{}
	}
	jsonResponse(w, entries)
}

// handleGetPendingVerdicts returns all verdicts with status 'pending'.
func (s *Server) handleGetPendingVerdicts(w http.ResponseWriter, r *http.Request) {
	verdicts, err := s.db.GetPendingVerdicts()
	if err != nil {
		jsonError(w, "failed to get pending verdicts", http.StatusInternalServerError)
		return
	}
	if verdicts == nil {
		verdicts = []db.Verdict{}
	}
	jsonResponse(w, verdicts)
}

// handleUpdateVerdictStatus updates the status of a verdict and optionally
// creates a training example for approved/rejected verdicts.
func (s *Server) handleUpdateVerdictStatus(w http.ResponseWriter, r *http.Request) {
	rawID := r.PathValue("messageID")
	messageID, err := url.PathUnescape(rawID)
	if err != nil {
		jsonError(w, "invalid message ID", http.StatusBadRequest)
		return
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	switch body.Status {
	case "approved", "rejected", "corrected":
		// valid
	default:
		jsonError(w, "status must be approved, rejected, or corrected", http.StatusBadRequest)
		return
	}

	// Fetch the verdict before updating so we can create a training example.
	verdict, err := s.db.GetVerdictByMessageID(messageID)
	if err != nil {
		jsonError(w, "failed to look up verdict", http.StatusInternalServerError)
		return
	}

	if err := s.db.UpdateVerdictStatus(messageID, body.Status); err != nil {
		jsonError(w, "failed to update verdict status", http.StatusInternalServerError)
		return
	}

	// Create a training example for approved or rejected verdicts.
	if verdict != nil && (body.Status == "approved" || body.Status == "rejected") {
		label := body.Status
		ex := db.TrainingExample{
			Subject:   verdict.Subject,
			Sender:    verdict.Sender,
			Label:     label,
			Source:    "dashboard",
			CreatedAt: time.Now().UTC(),
		}
		// Best-effort; don't fail the request if this errors.
		_ = s.db.AddTrainingExample(ex)
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": body.Status})
}

// handleGetCategories returns all categories.
func (s *Server) handleGetCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.db.GetCategories()
	if err != nil {
		jsonError(w, "failed to get categories", http.StatusInternalServerError)
		return
	}
	if cats == nil {
		cats = []db.Category{}
	}
	jsonResponse(w, cats)
}

// handleSaveCategory decodes a category from the request body and upserts it.
func (s *Server) handleSaveCategory(w http.ResponseWriter, r *http.Request) {
	var cat db.Category
	if err := json.NewDecoder(r.Body).Decode(&cat); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := s.db.SaveCategory(cat); err != nil {
		jsonError(w, "failed to save category", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, cat)
}

// handleDeleteCategory removes a category by its path parameter ID.
func (s *Server) handleDeleteCategory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteCategory(id); err != nil {
		jsonError(w, "failed to delete category", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetTrainingExamples returns training examples. Accepts ?category=X.
func (s *Server) handleGetTrainingExamples(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	examples, err := s.db.GetTrainingExamples(category)
	if err != nil {
		jsonError(w, "failed to get training examples", http.StatusInternalServerError)
		return
	}
	if examples == nil {
		examples = []db.TrainingExample{}
	}
	jsonResponse(w, examples)
}

// handleDeleteTrainingExample removes a training example by its path parameter ID.
func (s *Server) handleDeleteTrainingExample(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		jsonError(w, "invalid ID", http.StatusBadRequest)
		return
	}
	if err := s.db.DeleteTrainingExample(id); err != nil {
		jsonError(w, "failed to delete training example", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetStats returns aggregate statistics.
func (s *Server) handleGetStats(w http.ResponseWriter, r *http.Request) {
	pendingVerdicts, err := s.db.GetPendingVerdicts()
	if err != nil {
		jsonError(w, "failed to get stats", http.StatusInternalServerError)
		return
	}

	// Count today's moved and corrected entries from the activity log.
	// Use a generous limit and filter in memory to avoid schema changes.
	entries, err := s.db.GetActivityLog(1000)
	if err != nil {
		jsonError(w, "failed to get stats", http.StatusInternalServerError)
		return
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	var movedToday, correctedToday int
	for _, e := range entries {
		if e.CreatedAt.UTC().Before(today) {
			continue
		}
		switch e.Type {
		case "expired", "moved", "classified":
			movedToday++
		case "corrected":
			correctedToday++
		}
	}

	jsonResponse(w, map[string]int{
		"pendingVerdicts": len(pendingVerdicts),
		"movedToday":      movedToday,
		"correctedToday":  correctedToday,
	})
}
