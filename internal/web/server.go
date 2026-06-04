package web

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"marathon/internal/race"
)

type Server struct {
	mux       *http.ServeMux
	service   *race.Service
	templates *template.Template
	staticDir string
}

type Option func(*Server)

func WithStaticDir(path string) Option {
	return func(s *Server) {
		s.staticDir = path
	}
}

func WithTemplates(pattern string) Option {
	return func(s *Server) {
		s.templates = template.Must(template.ParseGlob(pattern))
	}
}

func NewServer(service *race.Service, options ...Option) *Server {
	server := &Server{
		mux:       http.NewServeMux(),
		service:   service,
		staticDir: "web/static",
	}
	for _, option := range options {
		option(server)
	}
	if server.templates == nil {
		server.templates = template.Must(template.ParseGlob(resolvePath(filepath.Join("web", "templates", "*.html"))))
	}
	server.staticDir = resolvePath(server.staticDir)
	server.routes()
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.staticDir))))
	s.mux.HandleFunc("GET /", s.dashboard)
	s.mux.HandleFunc("GET /runners/{bib}", s.runnerProfile)
	s.mux.HandleFunc("GET /api/state", s.state)
	s.mux.HandleFunc("POST /api/participants", s.registerParticipant)
	s.mux.HandleFunc("POST /api/checkpoint-logs", s.recordCheckpoint)
	s.mux.HandleFunc("GET /reports/final.csv", s.finalCSV)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Snapshot race.Snapshot
	}{
		Snapshot: s.service.Snapshot(),
	}
	if err := s.templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		http.Error(w, "dashboard could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) runnerProfile(w http.ResponseWriter, r *http.Request) {
	bib := r.PathValue("bib")
	profile, err := s.service.RunnerProfile(bib)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := struct {
		Event   race.Event
		Profile race.RunnerProfile
	}{
		Event:   s.service.Event(),
		Profile: profile,
	}
	if err := s.templates.ExecuteTemplate(w, "runner.html", data); err != nil {
		http.Error(w, "runner profile could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.service.Snapshot())
}

func (s *Server) registerParticipant(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		PhoneNumber string `json:"phoneNumber"`
		Notes       string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	participant, err := s.service.RegisterParticipant(input.Name, input.PhoneNumber, input.Notes)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, participant)
}

func (s *Server) recordCheckpoint(w http.ResponseWriter, r *http.Request) {
	var input struct {
		BibNumber    string `json:"bibNumber"`
		CheckpointID string `json:"checkpointId"`
		VolunteerID  string `json:"volunteerId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	log, err := s.service.RecordCheckpoint(input.BibNumber, input.CheckpointID, input.VolunteerID, time.Now().UTC())
	if err != nil {
		writeProblem(w, statusForRaceError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, log)
}

func (s *Server) finalCSV(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="final-results.csv"`)

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Rank", "Bib", "Name", "Status", "Latest Checkpoint", "Finish Time", "Race Time", "Gap"})
	for _, entry := range s.service.Leaderboard() {
		_ = writer.Write([]string{
			intString(entry.Rank),
			entry.BibNumber,
			entry.RunnerName,
			string(entry.Status),
			entry.LatestCheckpoint,
			entry.FinishTime,
			entry.RaceTime,
			entry.Gap,
		})
	}
	writer.Flush()
}

func statusForRaceError(err error) int {
	switch {
	case errors.Is(err, race.ErrInvalidBib), errors.Is(err, race.ErrInvalidCheckpoint):
		return http.StatusNotFound
	case errors.Is(err, race.ErrDuplicateEntry), errors.Is(err, race.ErrOutOfOrderEntry), errors.Is(err, race.ErrInvalidParticipant):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{
		"error": strings.TrimSpace(detail),
	})
}

func intString(value int) string {
	return strconv.Itoa(value)
}

func resolvePath(path string) string {
	if matches, _ := filepath.Glob(path); len(matches) > 0 {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	for _, prefix := range []string{"..", filepath.Join("..", "..")} {
		candidate := filepath.Join(prefix, path)
		if matches, _ := filepath.Glob(candidate); len(matches) > 0 {
			return candidate
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return path
}
