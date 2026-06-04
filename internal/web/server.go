package web

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"marathon/internal/importer"
	"marathon/internal/race"
)

type Server struct {
	mux          *http.ServeMux
	service      *race.Service
	projects     *projectRegistry
	projectStore race.Store
	templates    *template.Template
	staticDir    string
}

type projectRegistry struct {
	mu       sync.RWMutex
	activeID string
	ids      []string
	services map[string]*race.Service
}

type projectSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
	Active   bool   `json:"active"`
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

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

func WithProjectStore(store race.Store) Option {
	return func(s *Server) {
		s.projectStore = store
	}
}

func WithProjectServices(services []*race.Service) Option {
	return func(s *Server) {
		if len(services) == 0 {
			return
		}
		s.service = services[0]
		s.projects = newProjectRegistry(services[0])
		for _, service := range services[1:] {
			_ = s.addProject(service.Event().ID, service)
		}
	}
}

func NewServer(service *race.Service, options ...Option) *Server {
	server := &Server{
		mux:       http.NewServeMux(),
		service:   service,
		projects:  newProjectRegistry(service),
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
	s.mux.HandleFunc("GET /race", s.racePage)
	s.mux.HandleFunc("GET /runners/{bib}", s.runnerProfile)
	s.mux.HandleFunc("GET /events/{eventID}", s.dashboard)
	s.mux.HandleFunc("GET /events/{eventID}/race", s.racePage)
	s.mux.HandleFunc("GET /events/{eventID}/runners/{bib}", s.runnerProfile)
	s.mux.HandleFunc("GET /api/events", s.events)
	s.mux.HandleFunc("POST /api/events", s.createEvent)
	s.mux.HandleFunc("GET /api/state", s.state)
	s.mux.HandleFunc("GET /events/{eventID}/api/state", s.state)
	s.mux.HandleFunc("POST /api/event-settings", s.updateEventSettings)
	s.mux.HandleFunc("POST /events/{eventID}/api/event-settings", s.updateEventSettings)
	s.mux.HandleFunc("POST /api/checkpoints", s.addCheckpoint)
	s.mux.HandleFunc("POST /events/{eventID}/api/checkpoints", s.addCheckpoint)
	s.mux.HandleFunc("POST /api/participants", s.registerParticipant)
	s.mux.HandleFunc("POST /events/{eventID}/api/participants", s.registerParticipant)
	s.mux.HandleFunc("POST /api/import-runners", s.importRunners)
	s.mux.HandleFunc("POST /events/{eventID}/api/import-runners", s.importRunners)
	s.mux.HandleFunc("POST /api/checkpoint-logs", s.recordCheckpoint)
	s.mux.HandleFunc("POST /events/{eventID}/api/checkpoint-logs", s.recordCheckpoint)
	s.mux.HandleFunc("GET /reports/final.csv", s.finalCSV)
	s.mux.HandleFunc("GET /events/{eventID}/reports/final.csv", s.finalCSV)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	data := struct {
		Snapshot race.Snapshot
		Projects []projectSummary
		BasePath string
	}{
		Snapshot: service.Snapshot(),
		Projects: s.projectSummaries(service.Event().ID),
		BasePath: s.basePathFor(service.Event().ID),
	}
	if err := s.templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		http.Error(w, "dashboard could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) racePage(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	data := struct {
		Snapshot race.Snapshot
		Projects []projectSummary
		BasePath string
	}{
		Snapshot: service.Snapshot(),
		Projects: s.projectSummaries(service.Event().ID),
		BasePath: s.basePathFor(service.Event().ID),
	}
	if err := s.templates.ExecuteTemplate(w, "race.html", data); err != nil {
		http.Error(w, "race page could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) runnerProfile(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	bib := r.PathValue("bib")
	profile, err := service.RunnerProfile(bib)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := struct {
		Event   race.Event
		Profile race.RunnerProfile
	}{
		Event:   service.Event(),
		Profile: profile,
	}
	if err := s.templates.ExecuteTemplate(w, "runner.html", data); err != nil {
		http.Error(w, "runner profile could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.projectSummaries(s.projects.activeID))
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name       string `json:"name"`
		Location   string `json:"location"`
		DistanceKM int    `json:"distanceKm"`
		StartTime  string `json:"startTime"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "marathon name is required")
		return
	}
	if input.DistanceKM <= 0 {
		writeProblem(w, http.StatusUnprocessableEntity, "distance must be greater than zero")
		return
	}
	start, err := time.Parse(time.RFC3339, input.StartTime)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "start time must be an RFC3339 timestamp")
		return
	}
	id := slugify(name)
	event := race.Event{
		ID:          id,
		Name:        name,
		Description: "Marathon project",
		Date:        start,
		StartTime:   start,
		Location:    strings.TrimSpace(input.Location),
		DistanceKM:  input.DistanceKM,
		Status:      race.EventStatusUpcoming,
	}
	service := race.NewService(event, defaultCheckpoints(input.DistanceKM), nil, 10*time.Minute)
	if s.projectStore != nil {
		if err := service.UseStore(s.projectStore); err != nil {
			writeProblem(w, http.StatusInternalServerError, "marathon project could not be saved")
			return
		}
	}
	if err := s.addProject(id, service); err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, event)
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, service.Snapshot())
}

func (s *Server) registerParticipant(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	var input struct {
		Name        string `json:"name"`
		PhoneNumber string `json:"phoneNumber"`
		Notes       string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	participant, err := service.RegisterParticipant(input.Name, input.PhoneNumber, input.Notes)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, participant)
}

func (s *Server) updateEventSettings(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	var input struct {
		DistanceKM int    `json:"distanceKm"`
		StartTime  string `json:"startTime"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	start, err := time.Parse(time.RFC3339, input.StartTime)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "start time must be an RFC3339 timestamp")
		return
	}
	event, err := service.UpdateEventSettings(input.DistanceKM, start)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, event)
}

func (s *Server) importRunners(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		writeProblem(w, http.StatusBadRequest, "Upload must be a multipart form up to 20 MB.")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Runner import file is required.")
		return
	}
	defer file.Close()

	rows, err := importer.ParseUpload(file, header.Filename, importer.Mapping{
		BibColumn:   r.FormValue("bibColumn"),
		NameColumn:  r.FormValue("nameColumn"),
		PhoneColumn: r.FormValue("phoneColumn"),
		NotesColumn: r.FormValue("notesColumn"),
	})
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	result := service.ImportParticipants(rows)
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) addCheckpoint(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	var input struct {
		Name       string  `json:"name"`
		Sequence   int     `json:"sequence"`
		DistanceKM float64 `json:"distanceKm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	checkpoint, err := service.AddCheckpoint(input.Name, input.Sequence, input.DistanceKM)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, checkpoint)
}

func (s *Server) recordCheckpoint(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	var input struct {
		BibNumber    string `json:"bibNumber"`
		CheckpointID string `json:"checkpointId"`
		VolunteerID  string `json:"volunteerId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	log, err := service.RecordCheckpoint(input.BibNumber, input.CheckpointID, input.VolunteerID, time.Now().UTC())
	if err != nil {
		writeProblem(w, statusForRaceError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, log)
}

func (s *Server) finalCSV(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="final-results.csv"`)

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Rank", "Bib", "Name", "Status", "Latest Checkpoint", "Finish Time", "Race Time", "Gap"})
	for _, entry := range service.Leaderboard() {
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

func newProjectRegistry(service *race.Service) *projectRegistry {
	defaultID := service.Event().ID
	return &projectRegistry{
		activeID: defaultID,
		ids:      []string{defaultID},
		services: map[string]*race.Service{defaultID: service},
	}
}

func (s *Server) serviceForRequest(w http.ResponseWriter, r *http.Request) (*race.Service, bool) {
	eventID := r.PathValue("eventID")
	if eventID == "" {
		return s.service, true
	}
	s.projects.mu.RLock()
	service, ok := s.projects.services[eventID]
	s.projects.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return nil, false
	}
	return service, true
}

func (s *Server) addProject(id string, service *race.Service) error {
	s.projects.mu.Lock()
	defer s.projects.mu.Unlock()
	if _, exists := s.projects.services[id]; exists {
		return errors.New("a marathon project with this name already exists")
	}
	s.projects.services[id] = service
	s.projects.ids = append(s.projects.ids, id)
	return nil
}

func (s *Server) projectSummaries(activeID string) []projectSummary {
	s.projects.mu.RLock()
	defer s.projects.mu.RUnlock()
	summaries := make([]projectSummary, 0, len(s.projects.ids))
	for _, id := range s.projects.ids {
		service := s.projects.services[id]
		event := service.Event()
		summaries = append(summaries, projectSummary{
			ID:       event.ID,
			Name:     event.Name,
			Location: event.Location,
			Active:   event.ID == activeID,
		})
	}
	return summaries
}

func (s *Server) basePathFor(eventID string) string {
	if eventID == s.projects.activeID {
		return ""
	}
	return "/events/" + eventID
}

func defaultCheckpoints(distanceKM int) []race.Checkpoint {
	midpoint := float64(distanceKM) / 2
	return []race.Checkpoint{
		{ID: "start", Name: "Start", Sequence: 1, DistanceKM: 0},
		{ID: "cp1", Name: "CP1", Sequence: 2, DistanceKM: midpoint / 2},
		{ID: "cp2", Name: "CP2", Sequence: 3, DistanceKM: midpoint},
		{ID: "finish", Name: "Finish", Sequence: 4, DistanceKM: float64(distanceKM)},
	}
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonSlugChars.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "marathon"
	}
	return value
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
