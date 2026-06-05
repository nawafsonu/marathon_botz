package web

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
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

	"marathon/internal/analysis"
	"marathon/internal/auth"
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
	authManager  *auth.Manager
	analyzer     *analysis.Client
	sessionMu    sync.RWMutex
	sessions     map[string]string
}

type projectRegistry struct {
	mu       sync.RWMutex
	activeID string
	ids      []string
	services map[string]*race.Service
}

type projectSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Location     string `json:"location"`
	Active       bool   `json:"active"`
	DashboardURL string `json:"dashboardUrl"`
	RaceURL      string `json:"raceUrl"`
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
			s.service = nil
			s.projects = newProjectRegistry(nil)
			return
		}
		s.service = services[0]
		s.projects = newProjectRegistry(services[0])
		for _, service := range services[1:] {
			_ = s.addProject(service.Event().ID, service)
		}
	}
}

func WithAuthManager(manager *auth.Manager) Option {
	return func(s *Server) {
		s.authManager = manager
	}
}

func WithAnalyzer(analyzer *analysis.Client) Option {
	return func(s *Server) {
		s.analyzer = analyzer
	}
}

func NewServer(service *race.Service, options ...Option) *Server {
	server := &Server{
		mux:       http.NewServeMux(),
		service:   service,
		projects:  newProjectRegistry(service),
		staticDir: "web/static",
		sessions:  make(map[string]string),
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
	if !s.authorize(w, r) {
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.staticDir))))
	s.mux.HandleFunc("GET /login", s.loginPage)
	s.mux.HandleFunc("POST /login", s.login)
	s.mux.HandleFunc("POST /logout", s.logout)
	s.mux.HandleFunc("GET /", s.dashboard)
	s.mux.HandleFunc("GET /race", s.racePage)
	s.mux.HandleFunc("GET /runners/{bib}", s.runnerProfile)
	s.mux.HandleFunc("GET /events/{eventID}", s.dashboard)
	s.mux.HandleFunc("GET /events/{eventID}/race", s.racePage)
	s.mux.HandleFunc("GET /events/{eventID}/runners/{bib}", s.runnerProfile)
	s.mux.HandleFunc("GET /api/events", s.events)
	s.mux.HandleFunc("POST /api/events", s.createEvent)
	s.mux.HandleFunc("POST /api/volunteers", s.createVolunteer)
	s.mux.HandleFunc("POST /api/volunteers/{username}/delete", s.deleteVolunteer)
	s.mux.HandleFunc("GET /api/state", s.state)
	s.mux.HandleFunc("GET /events/{eventID}/api/state", s.state)
	s.mux.HandleFunc("POST /api/event-settings", s.updateEventSettings)
	s.mux.HandleFunc("POST /events/{eventID}/api/event-settings", s.updateEventSettings)
	s.mux.HandleFunc("POST /api/start-race", s.startRace)
	s.mux.HandleFunc("POST /events/{eventID}/api/start-race", s.startRace)
	s.mux.HandleFunc("POST /api/checkpoints", s.addCheckpoint)
	s.mux.HandleFunc("POST /events/{eventID}/api/checkpoints", s.addCheckpoint)
	s.mux.HandleFunc("POST /api/participants", s.registerParticipant)
	s.mux.HandleFunc("POST /events/{eventID}/api/participants", s.registerParticipant)
	s.mux.HandleFunc("POST /api/import-runners", s.importRunners)
	s.mux.HandleFunc("POST /events/{eventID}/api/import-runners", s.importRunners)
	s.mux.HandleFunc("POST /api/checkpoint-logs", s.recordCheckpoint)
	s.mux.HandleFunc("POST /events/{eventID}/api/checkpoint-logs", s.recordCheckpoint)
	s.mux.HandleFunc("POST /api/analysis/race", s.analyzeRace)
	s.mux.HandleFunc("POST /events/{eventID}/api/analysis/race", s.analyzeRace)
	s.mux.HandleFunc("POST /api/analysis/runners/{bib}", s.analyzeRunner)
	s.mux.HandleFunc("POST /events/{eventID}/api/analysis/runners/{bib}", s.analyzeRunner)
	s.mux.HandleFunc("GET /reports/final.csv", s.finalCSV)
	s.mux.HandleFunc("GET /events/{eventID}/reports/final.csv", s.finalCSV)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	var service *race.Service
	var ok bool
	if r.PathValue("eventID") == "" && s.service == nil {
		ok = true
	} else {
		service, ok = s.serviceForRequest(w, r)
	}
	if !ok {
		return
	}
	snapshot := race.Snapshot{}
	basePath := ""
	activeID := ""
	if service != nil {
		snapshot = service.Snapshot()
		basePath = s.basePathFor(service.Event().ID)
		activeID = service.Event().ID
	}
	data := struct {
		Snapshot    race.Snapshot
		Projects    []projectSummary
		BasePath    string
		User        auth.User
		Volunteers  []auth.User
		AuthEnabled bool
		CanManage   bool
		AIEnabled   bool
	}{
		Snapshot:    snapshot,
		Projects:    s.projectSummaries(activeID),
		BasePath:    basePath,
		User:        s.currentUser(r),
		Volunteers:  s.volunteers(),
		AuthEnabled: s.authManager != nil,
		CanManage:   s.canManage(r),
		AIEnabled:   s.analyzer != nil,
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
		Snapshot    race.Snapshot
		Projects    []projectSummary
		BasePath    string
		User        auth.User
		AuthEnabled bool
		CanManage   bool
	}{
		Snapshot:    service.Snapshot(),
		Projects:    s.projectSummaries(service.Event().ID),
		BasePath:    s.basePathFor(service.Event().ID),
		User:        s.currentUser(r),
		AuthEnabled: s.authManager != nil,
		CanManage:   s.canManage(r),
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
		BasePath string
		AIEnabled bool
	}{
		Event:     service.Event(),
		Profile:   profile,
		BasePath:  s.basePathFor(service.Event().ID),
		AIEnabled: s.analyzer != nil,
	}
	if err := s.templates.ExecuteTemplate(w, "runner.html", data); err != nil {
		http.Error(w, "runner profile could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.authManager == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	data := struct {
		Error string
	}{}
	if err := s.templates.ExecuteTemplate(w, "login.html", data); err != nil {
		http.Error(w, "login page could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.authManager == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "login form could not be read", http.StatusBadRequest)
		return
	}
	user, ok := s.authManager.Authenticate(r.FormValue("username"), r.FormValue("password"))
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_ = s.templates.ExecuteTemplate(w, "login.html", struct{ Error string }{Error: "Invalid username or password"})
		return
	}
	token, err := sessionToken()
	if err != nil {
		http.Error(w, "session could not be created", http.StatusInternalServerError)
		return
	}
	s.sessionMu.Lock()
	s.sessions[token] = user.Username
	s.sessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     "mt_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("mt_session"); err == nil {
		s.sessionMu.Lock()
		delete(s.sessions, cookie.Value)
		s.sessionMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "mt_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.projectSummaries(s.projects.activeID))
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
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

func (s *Server) createVolunteer(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.authManager == nil {
		writeProblem(w, http.StatusNotFound, "volunteer management is not enabled")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	user, err := s.authManager.AddVolunteer(input.Username, input.Password)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (s *Server) deleteVolunteer(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.authManager == nil {
		writeProblem(w, http.StatusNotFound, "volunteer management is not enabled")
		return
	}
	if err := s.authManager.DeleteVolunteer(r.PathValue("username")); err != nil {
		writeProblem(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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
	if !s.requireAdmin(w, r) {
		return
	}
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

func (s *Server) startRace(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	event, err := service.StartRace()
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, event)
}

func (s *Server) importRunners(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
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
	if !s.requireAdmin(w, r) {
		return
	}
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
		BibNumber     string `json:"bibNumber"`
		ParticipantID string `json:"participantId"`
		CheckpointID  string `json:"checkpointId"`
		VolunteerID   string `json:"volunteerId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	bibNumber := strings.TrimSpace(input.BibNumber)
	participantID := strings.TrimSpace(input.ParticipantID)
	if participantID != "" {
		participant, found := participantByID(service.Participants(), participantID)
		if !found {
			writeProblem(w, http.StatusNotFound, "selected runner was not found")
			return
		}
		bibNumber = participant.BibNumber
	}
	log, err := service.RecordCheckpoint(bibNumber, input.CheckpointID, input.VolunteerID, time.Now().UTC())
	if err != nil {
		writeProblem(w, statusForRaceError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, log)
}

func participantByID(participants []race.Participant, id string) (race.Participant, bool) {
	for _, participant := range participants {
		if participant.ID == id {
			return participant, true
		}
	}
	return race.Participant{}, false
}

func (s *Server) analyzeRace(w http.ResponseWriter, r *http.Request) {
	if s.analyzer == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Groq analysis is not configured")
		return
	}
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	text, err := s.analyzer.AnalyzeRace(ctx, service.Snapshot())
	if err != nil {
		writeProblem(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"analysis": text})
}

func (s *Server) analyzeRunner(w http.ResponseWriter, r *http.Request) {
	if s.analyzer == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Groq analysis is not configured")
		return
	}
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	profile, err := service.RunnerProfile(r.PathValue("bib"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, race.ErrInvalidBib.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	text, err := s.analyzer.AnalyzeRunner(ctx, service.Event(), profile)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"analysis": text})
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
	if service == nil {
		return &projectRegistry{
			services: make(map[string]*race.Service),
		}
	}
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
		if s.service == nil {
			http.NotFound(w, r)
			return nil, false
		}
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
	if s.projects.services == nil {
		s.projects.services = make(map[string]*race.Service)
	}
	s.projects.services[id] = service
	s.projects.ids = append(s.projects.ids, id)
	if s.projects.activeID == "" {
		s.projects.activeID = id
		s.service = service
	}
	return nil
}

func (s *Server) projectSummaries(activeID string) []projectSummary {
	s.projects.mu.RLock()
	defer s.projects.mu.RUnlock()
	summaries := make([]projectSummary, 0, len(s.projects.ids))
	for _, id := range s.projects.ids {
		service := s.projects.services[id]
		event := service.Event()
		raceURL := "/events/" + event.ID + "/race"
		dashboardURL := "/events/" + event.ID
		if event.ID == s.projects.activeID {
			raceURL = "/race"
			dashboardURL = "/"
		}
		summaries = append(summaries, projectSummary{
			ID:           event.ID,
			Name:         event.Name,
			Location:     event.Location,
			Active:       event.ID == activeID,
			DashboardURL: dashboardURL,
			RaceURL:      raceURL,
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

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.authManager == nil {
		return true
	}
	if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/static/") {
		return true
	}
	if _, ok := s.authenticatedUser(r); ok {
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.Contains(r.URL.Path, "/api/") {
		writeProblem(w, http.StatusUnauthorized, "login is required")
		return false
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
	return false
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.authManager == nil {
		return true
	}
	user, ok := s.authenticatedUser(r)
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "login is required")
		return false
	}
	if user.Role != auth.RoleAdmin {
		writeProblem(w, http.StatusForbidden, "admin access is required")
		return false
	}
	return true
}

func (s *Server) currentUser(r *http.Request) auth.User {
	user, _ := s.authenticatedUser(r)
	return user
}

func (s *Server) canManage(r *http.Request) bool {
	if s.authManager == nil {
		return true
	}
	user, ok := s.authenticatedUser(r)
	return ok && user.Role == auth.RoleAdmin
}

func (s *Server) authenticatedUser(r *http.Request) (auth.User, bool) {
	if s.authManager == nil {
		return auth.User{}, false
	}
	cookie, err := r.Cookie("mt_session")
	if err != nil || cookie.Value == "" {
		return auth.User{}, false
	}
	s.sessionMu.RLock()
	username, ok := s.sessions[cookie.Value]
	s.sessionMu.RUnlock()
	if !ok {
		return auth.User{}, false
	}
	return s.authManager.User(username)
}

func (s *Server) volunteers() []auth.User {
	if s.authManager == nil {
		return nil
	}
	return s.authManager.Volunteers()
}

func sessionToken() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
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
