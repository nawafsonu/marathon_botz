package web

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"marathon/internal/analysis"
	"marathon/internal/auth"
	"marathon/internal/chestreader"
	"marathon/internal/importer"
	"marathon/internal/race"
)

type Server struct {
	mux            *http.ServeMux
	service        *race.Service
	projects       *projectRegistry
	projectStore   race.Store
	templates      *template.Template
	staticDir      string
	authManager    *auth.Manager
	analyzer       *analysis.Client
	chestReader    *chestreader.Client
	chestReaderMu  sync.RWMutex
	sessionMu      sync.RWMutex
	sessionPath    string
	sessions       map[string]sessionRecord
	// stationStatus tracks per-checkpoint station state (in-memory, not persisted).
	// Key format: "eventID:checkpointID", value: "upcoming" | "active" | "completed"
	stationStatus  map[string]string
	stationMu      sync.RWMutex
}

type projectRegistry struct {
	mu       sync.RWMutex
	activeID string
	ids      []string
	services map[string]*race.Service
}

type projectSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Location       string `json:"location"`
	Active         bool   `json:"active"`
	DashboardURL   string `json:"dashboardUrl"`
	RaceURL        string `json:"raceUrl"`
	LeaderboardURL string `json:"leaderboardUrl"`
	MarathonID     string `json:"marathonId"`
	MarathonName   string `json:"marathonName"`
}

type sessionRecord struct {
	Username  string
	ExpiresAt time.Time
}

type certificateView struct {
	Event            race.Event
	Profile          race.RunnerProfile
	BasePath         string
	CertificateTitle string
	IsRanked         bool
	IssuedAt         time.Time
}

type projectDeleter interface {
	Delete(ctx context.Context, id string) error
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

type Option func(*Server)

const (
	sessionCookieName = "mt_session"
	sessionTTL        = 7 * 24 * time.Hour
)

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
		if manager != nil {
			s.sessionPath = manager.SessionPath()
		}
	}
}

func WithAnalyzer(analyzer *analysis.Client) Option {
	return func(s *Server) {
		s.analyzer = analyzer
	}
}

func WithChestReader(reader *chestreader.Client) Option {
	return func(s *Server) {
		s.chestReader = reader
	}
}

func NewServer(service *race.Service, options ...Option) *Server {
	server := &Server{
		mux:           http.NewServeMux(),
		service:       service,
		projects:      newProjectRegistry(service),
		staticDir:     "web/static",
		sessions:      make(map[string]sessionRecord),
		stationStatus: make(map[string]string),
	}
	for _, option := range options {
		option(server)
	}
	server.loadSessions()
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
	s.mux.HandleFunc("GET /guest-login", s.guestLoginPage)
	s.mux.HandleFunc("POST /guest-login", s.guestLogin)
	s.mux.HandleFunc("POST /logout", s.logout)
	s.mux.HandleFunc("GET /", s.dashboard)
	s.mux.HandleFunc("GET /race", s.racePage)
	s.mux.HandleFunc("GET /leaderboard", s.leaderboardPage)
	s.mux.HandleFunc("GET /certificates", s.bulkCertificates)
	s.mux.HandleFunc("GET /runners/{bib}", s.runnerProfile)
	s.mux.HandleFunc("GET /runners/{bib}/certificate", s.runnerCertificate)
	s.mux.HandleFunc("GET /events/{eventID}", s.dashboard)
	s.mux.HandleFunc("GET /events/{eventID}/race", s.racePage)
	s.mux.HandleFunc("GET /events/{eventID}/leaderboard", s.leaderboardPage)
	s.mux.HandleFunc("GET /events/{eventID}/certificates", s.bulkCertificates)
	s.mux.HandleFunc("GET /events/{eventID}/runners/{bib}", s.runnerProfile)
	s.mux.HandleFunc("GET /events/{eventID}/runners/{bib}/certificate", s.runnerCertificate)
	s.mux.HandleFunc("GET /api/events", s.events)
	s.mux.HandleFunc("POST /api/events", s.createEvent)
	s.mux.HandleFunc("POST /api/events/{eventID}/delete", s.deleteEvent)
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
	s.mux.HandleFunc("PATCH /api/checkpoints/{checkpointID}", s.updateCheckpoint)
	s.mux.HandleFunc("PATCH /events/{eventID}/api/checkpoints/{checkpointID}", s.updateCheckpoint)
	s.mux.HandleFunc("POST /api/checkpoints/{checkpointID}/delete", s.deleteCheckpoint)
	s.mux.HandleFunc("POST /events/{eventID}/api/checkpoints/{checkpointID}/delete", s.deleteCheckpoint)
	s.mux.HandleFunc("POST /api/checkpoints/{checkpointID}/station", s.setStationStatus)
	s.mux.HandleFunc("POST /events/{eventID}/api/checkpoints/{checkpointID}/station", s.setStationStatus)
	s.mux.HandleFunc("POST /api/participants", s.registerParticipant)
	s.mux.HandleFunc("POST /events/{eventID}/api/participants", s.registerParticipant)
	s.mux.HandleFunc("POST /api/participants/{bib}/delete", s.deleteParticipant)
	s.mux.HandleFunc("POST /events/{eventID}/api/participants/{bib}/delete", s.deleteParticipant)
	s.mux.HandleFunc("POST /api/import-runners", s.importRunners)
	s.mux.HandleFunc("POST /events/{eventID}/api/import-runners", s.importRunners)
	s.mux.HandleFunc("POST /api/chest-reader/config", s.configureChestReader)
	s.mux.HandleFunc("POST /events/{eventID}/api/chest-reader/config", s.configureChestReader)
	s.mux.HandleFunc("POST /api/chest-reader/scan", s.scanChestNumber)
	s.mux.HandleFunc("POST /events/{eventID}/api/chest-reader/scan", s.scanChestNumber)
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
		Snapshot           race.Snapshot
		Projects           []projectSummary
		BasePath           string
		User               auth.User
		AuthEnabled        bool
		CanManage          bool
		ChestReaderEnabled bool
	}{
		Snapshot:           service.Snapshot(),
		Projects:           s.projectSummaries(service.Event().ID),
		BasePath:           s.basePathFor(service.Event().ID),
		User:               s.currentUser(r),
		AuthEnabled:        s.authManager != nil,
		CanManage:          s.canManage(r),
		ChestReaderEnabled: s.currentChestReader() != nil,
	}
	if err := s.templates.ExecuteTemplate(w, "race.html", data); err != nil {
		http.Error(w, "race page could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) leaderboardPage(w http.ResponseWriter, r *http.Request) {
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
	}{
		Snapshot:    service.Snapshot(),
		Projects:    s.projectSummaries(service.Event().ID),
		BasePath:    s.basePathFor(service.Event().ID),
		User:        s.currentUser(r),
		AuthEnabled: s.authManager != nil,
	}
	if err := s.templates.ExecuteTemplate(w, "leaderboard.html", data); err != nil {
		http.Error(w, "leaderboard could not be rendered", http.StatusInternalServerError)
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
		Event     race.Event
		Profile   race.RunnerProfile
		BasePath  string
		AIEnabled bool
		CanManage bool
	}{
		Event:     service.Event(),
		Profile:   profile,
		BasePath:  s.basePathFor(service.Event().ID),
		AIEnabled: s.analyzer != nil,
		CanManage: s.canManage(r),
	}
	if err := s.templates.ExecuteTemplate(w, "runner.html", data); err != nil {
		http.Error(w, "runner profile could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) runnerCertificate(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	profile, err := service.RunnerProfile(r.PathValue("bib"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := buildCertificateView(service.Event(), s.basePathFor(service.Event().ID), profile, time.Now().UTC())
	if err := s.templates.ExecuteTemplate(w, "certificate.html", data); err != nil {
		http.Error(w, "runner certificate could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) bulkCertificates(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	event := service.Event()
	basePath := s.basePathFor(event.ID)
	issuedAt := time.Now().UTC()
	certificates := make([]certificateView, 0)
	for _, entry := range service.Leaderboard() {
		if entry.Status != race.RaceStatusFinished {
			continue
		}
		profile, err := service.RunnerProfile(entry.BibNumber)
		if err != nil {
			continue
		}
		certificates = append(certificates, buildCertificateView(event, basePath, profile, issuedAt))
	}
	data := struct {
		Event        race.Event
		BasePath     string
		Certificates []certificateView
		IssuedAt     time.Time
	}{
		Event:        event,
		BasePath:     basePath,
		Certificates: certificates,
		IssuedAt:     issuedAt,
	}
	if err := s.templates.ExecuteTemplate(w, "bulk_certificates.html", data); err != nil {
		http.Error(w, "bulk certificates could not be rendered", http.StatusInternalServerError)
	}
}

func buildCertificateView(event race.Event, basePath string, profile race.RunnerProfile, issuedAt time.Time) certificateView {
	title := "Certificate of Participation"
	isRanked := false
	if profile.Participant.Status == race.RaceStatusFinished {
		title = "Certificate of Completion"
		isRanked = profile.Summary.Rank > 0
	}
	if isRanked {
		title = "Ranked Finisher Certificate"
	}
	return certificateView{
		Event:            event,
		Profile:          profile,
		BasePath:         basePath,
		CertificateTitle: title,
		IsRanked:         isRanked,
		IssuedAt:         issuedAt,
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

func (s *Server) guestLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.authManager == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	data := struct {
		Error string
		Bib   string
		Phone string
	}{}
	if err := s.templates.ExecuteTemplate(w, "guest_login.html", data); err != nil {
		http.Error(w, "guest login page could not be rendered", http.StatusInternalServerError)
	}
}

func (s *Server) guestLogin(w http.ResponseWriter, r *http.Request) {
	if s.authManager == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form could not be read", http.StatusBadRequest)
		return
	}

	bib := strings.TrimSpace(r.FormValue("bib"))
	phone := strings.TrimSpace(r.FormValue("phone"))

	renderError := func(msg string) {
		w.WriteHeader(http.StatusUnauthorized)
		data := struct {
			Error string
			Bib   string
			Phone string
		}{Error: msg, Bib: bib, Phone: phone}
		_ = s.templates.ExecuteTemplate(w, "guest_login.html", data)
	}

	if bib == "" || phone == "" {
		renderError("Bib number and phone number are required.")
		return
	}

	// Validate bib + phone against registered participants across all events.
	bibPath, ok := s.authenticateGuest(bib, phone)
	if !ok {
		renderError("No matching runner found. Please check your bib number and phone number.")
		return
	}

	token, err := sessionToken()
	if err != nil {
		http.Error(w, "session could not be created", http.StatusInternalServerError)
		return
	}
	// Guest sessions last 24 hours and are NOT persisted to disk.
	const guestTTL = 24 * time.Hour
	expiresAt := time.Now().UTC().Add(guestTTL)
	s.sessionMu.Lock()
	s.sessions[token] = sessionRecord{Username: "__guest__", ExpiresAt: expiresAt}
	s.sessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(guestTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	// Redirect directly to the runner's certificate.
	http.Redirect(w, r, bibPath, http.StatusSeeOther)
}

// authenticateGuest looks up a participant by bib number across all loaded race
// services and checks if the supplied phone number matches. Returns the
// certificate URL for that runner on success.
func (s *Server) authenticateGuest(bib, phone string) (string, bool) {
	normBib := strings.ToUpper(strings.TrimSpace(bib))
	normPhone := normalizePhone(phone)
	if normBib == "" || normPhone == "" {
		return "", false
	}

	s.projects.mu.RLock()
	services := make([]*race.Service, 0, len(s.projects.ids))
	for _, id := range s.projects.ids {
		services = append(services, s.projects.services[id])
	}
	activeID := s.projects.activeID
	s.projects.mu.RUnlock()

	for _, svc := range services {
		for _, p := range svc.Participants() {
			if strings.ToUpper(strings.TrimSpace(p.BibNumber)) != normBib {
				continue
			}
			if normalizePhone(p.PhoneNumber) != normPhone {
				continue
			}
			// Match — build the certificate URL.
			eventID := svc.Event().ID
			base := ""
			if eventID != activeID {
				base = "/events/" + eventID
			}
			return base + "/runners/" + p.BibNumber + "/certificate", true
		}
	}
	return "", false
}

// normalizePhone strips all non-digit characters so "9876543210", "+91 98765 43210",
// and "98765-43210" all compare equal.
func normalizePhone(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
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
	expiresAt := time.Now().UTC().Add(sessionTTL)
	s.sessionMu.Lock()
	s.sessions[token] = sessionRecord{Username: user.Username, ExpiresAt: expiresAt}
	if err := s.saveSessionsLocked(); err != nil {
		delete(s.sessions, token)
		s.sessionMu.Unlock()
		http.Error(w, "session could not be saved", http.StatusInternalServerError)
		return
	}
	s.sessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.sessionMu.Lock()
		delete(s.sessions, cookie.Value)
		_ = s.saveSessionsLocked()
		s.sessionMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.projectSummaries(s.projects.activeID))
}

type raceInput struct {
	Name        string `json:"name"`
	DistanceKM  int    `json:"distanceKm"`
	StartTime   string `json:"startTime"`
	Checkpoints int    `json:"checkpoints"`
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var input struct {
		Name     string      `json:"name"`
		Location string      `json:"location"`
		Races    []raceInput `json:"races"`
		// Legacy single-race fields, kept so older clients keep working.
		DistanceKM int      `json:"distanceKm"`
		StartTime  string   `json:"startTime"`
		Categories []string `json:"categories"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	if s.projectStore == nil {
		writeProblem(w, http.StatusServiceUnavailable, "MongoDB persistence is required before creating a marathon")
		return
	}
	marathonName := strings.TrimSpace(input.Name)
	if marathonName == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "marathon name is required")
		return
	}
	location := strings.TrimSpace(input.Location)

	// Legacy path: no races[] supplied, so synthesize one ungrouped race from the
	// top-level distance/start/categories. This keeps Event.ID == slugify(name).
	legacy := len(input.Races) == 0
	if legacy {
		input.Races = []raceInput{{
			Name:        "",
			DistanceKM:  input.DistanceKM,
			StartTime:   input.StartTime,
			Checkpoints: 2,
		}}
	}

	marathonID := slugify(marathonName)
	type preparedRace struct {
		event       race.Event
		checkpoints []race.Checkpoint
	}
	prepared := make([]preparedRace, 0, len(input.Races))
	usedIDs := make(map[string]bool, len(input.Races))

	// Validate every race before creating any, so a bad race never leaves a
	// half-built marathon behind.
	for i, rc := range input.Races {
		raceName := strings.TrimSpace(rc.Name)
		if !legacy && raceName == "" {
			writeProblem(w, http.StatusUnprocessableEntity, fmt.Sprintf("race %d name is required", i+1))
			return
		}
		if rc.DistanceKM <= 0 {
			writeProblem(w, http.StatusUnprocessableEntity, raceLabel(raceName, i)+": distance must be greater than zero")
			return
		}
		if rc.Checkpoints < 0 {
			writeProblem(w, http.StatusUnprocessableEntity, raceLabel(raceName, i)+": number of checkpoints cannot be negative")
			return
		}
		start, err := time.Parse(time.RFC3339, rc.StartTime)
		if err != nil {
			writeProblem(w, http.StatusUnprocessableEntity, raceLabel(raceName, i)+": start time must be an RFC3339 timestamp")
			return
		}

		event := race.Event{
			Date:       start,
			StartTime:  start,
			Location:   location,
			DistanceKM: rc.DistanceKM,
			Status:     race.EventStatusUpcoming,
		}
		if legacy {
			event.ID = s.uniqueProjectID(marathonID, usedIDs)
			event.Name = marathonName
			event.Description = "Marathon project"
			categories := filterCategories(input.Categories)
			if len(categories) == 0 {
				categories = defaultCategories(rc.DistanceKM)
			}
			event.Categories = categories
		} else {
			event.ID = s.uniqueProjectID(marathonID+"-"+slugify(raceName), usedIDs)
			event.Name = marathonName + " · " + raceName
			event.Description = "Race in " + marathonName
			event.Categories = []string{raceName}
			event.MarathonID = marathonID
			event.MarathonName = marathonName
		}
		prepared = append(prepared, preparedRace{
			event:       event,
			checkpoints: race.GenerateCheckpoints(float64(rc.DistanceKM), rc.Checkpoints),
		})
	}

	created := make([]race.Event, 0, len(prepared))
	for _, p := range prepared {
		service := race.NewService(p.event, p.checkpoints, nil, 10*time.Minute)
		if err := service.UseStore(s.projectStore); err != nil {
			writeProblem(w, http.StatusInternalServerError, "marathon project could not be saved")
			return
		}
		if err := s.addProject(p.event.ID, service); err != nil {
			writeProblem(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		created = append(created, p.event)
	}

	if legacy {
		writeJSON(w, http.StatusCreated, created[0])
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"marathonId":   marathonID,
		"marathonName": marathonName,
		"races":        created,
	})
}

func raceLabel(name string, index int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("race %d", index+1)
}

// uniqueProjectID returns base unless it collides with an existing project or one
// already chosen in this batch, in which case it appends -2, -3, …
func (s *Server) uniqueProjectID(base string, used map[string]bool) string {
	if base == "" {
		base = "race"
	}
	id := base
	for n := 2; used[id] || s.hasProject(id); n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	used[id] = true
	return id
}

func (s *Server) deleteEvent(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	eventID := strings.TrimSpace(r.PathValue("eventID"))
	if eventID == "" {
		writeProblem(w, http.StatusBadRequest, "marathon id is required")
		return
	}
	deleter, ok := s.projectStore.(projectDeleter)
	if !ok {
		writeProblem(w, http.StatusServiceUnavailable, "MongoDB persistence is required before deleting a marathon")
		return
	}
	if !s.hasProject(eventID) {
		http.NotFound(w, r)
		return
	}
	if err := deleter.Delete(r.Context(), eventID); err != nil {
		writeProblem(w, http.StatusInternalServerError, "marathon project could not be deleted")
		return
	}
	redirect, ok := s.removeProject(eventID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "redirect": redirect})
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
	snapshot := service.Snapshot()
	// Inject in-memory station status into each checkpoint.
	eventID := service.Event().ID
	s.stationMu.RLock()
	for i, cp := range snapshot.Checkpoints {
		key := eventID + ":" + cp.ID
		if status, ok := s.stationStatus[key]; ok {
			snapshot.Checkpoints[i].StationStatus = status
		}
	}
	s.stationMu.RUnlock()
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) registerParticipant(w http.ResponseWriter, r *http.Request) {
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	var input struct {
		BibNumber   string `json:"bibNumber"`
		Name        string `json:"name"`
		PhoneNumber string `json:"phoneNumber"`
		Category    string `json:"category"`
		Notes       string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	event := service.Event()
	// One bib = one runner across every race in the marathon.
	if raceName, taken := s.marathonBibTaken(event.MarathonID, event.ID, input.BibNumber); taken {
		writeProblem(w, http.StatusUnprocessableEntity, fmt.Sprintf("bib %s is already registered in %s", race.NormalizeBib(input.BibNumber), raceName))
		return
	}
	// The selected race already determines the category, so volunteers don't pick
	// one: default to the race's category when the client doesn't send it.
	category := strings.TrimSpace(input.Category)
	if category == "" {
		if cats := event.Categories; len(cats) > 0 {
			category = cats[0]
		}
	}
	participant, err := service.RegisterParticipantWithBib(input.BibNumber, input.Name, input.PhoneNumber, category, input.Notes)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, participant)
}

func (s *Server) deleteParticipant(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	if err := service.DeleteParticipant(r.PathValue("bib")); err != nil {
		writeProblem(w, statusForRaceError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "redirect": s.basePathFor(service.Event().ID)})
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
	// Body is optional: an empty body (or no "category") starts every runner.
	var input struct {
		Category string `json:"category"`
	}
	_ = json.NewDecoder(r.Body).Decode(&input)
	event, err := service.StartRaceCategory(input.Category)
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
		BibColumn:      r.FormValue("bibColumn"),
		NameColumn:     r.FormValue("nameColumn"),
		PhoneColumn:    r.FormValue("phoneColumn"),
		CategoryColumn: r.FormValue("categoryColumn"),
		NotesColumn:    r.FormValue("notesColumn"),
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

func (s *Server) updateCheckpoint(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	checkpointID := strings.TrimSpace(r.PathValue("checkpointID"))
	var input struct {
		DistanceKM float64 `json:"distanceKm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	checkpoint, err := service.UpdateCheckpointDistance(checkpointID, input.DistanceKM)
	if err != nil {
		writeProblem(w, statusForRaceError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, checkpoint)
}

func (s *Server) deleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	checkpointID := strings.TrimSpace(r.PathValue("checkpointID"))
	if err := service.DeleteCheckpoint(checkpointID); err != nil {
		writeProblem(w, statusForRaceError(err), err.Error())
		return
	}
	// Clean up station status for deleted checkpoint.
	eventID := service.Event().ID
	s.stationMu.Lock()
	delete(s.stationStatus, eventID+":"+checkpointID)
	s.stationMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) setStationStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}
	checkpointID := strings.TrimSpace(r.PathValue("checkpointID"))
	var input struct {
		Status string `json:"status"` // upcoming | active | completed
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	status := strings.TrimSpace(input.Status)
	if status != "upcoming" && status != "active" && status != "completed" {
		writeProblem(w, http.StatusBadRequest, "status must be upcoming, active, or completed")
		return
	}

	// Validate checkpoint exists.
	checkpoints := service.Checkpoints()
	var found *race.Checkpoint
	for i := range checkpoints {
		if checkpoints[i].ID == checkpointID {
			found = &checkpoints[i]
			break
		}
	}
	if found == nil {
		http.NotFound(w, r)
		return
	}

	eventID := service.Event().ID
	s.stationMu.Lock()
	s.stationStatus[eventID+":"+checkpointID] = status
	s.stationMu.Unlock()

	// If the Finish checkpoint is closed → auto-complete the event.
	isFinish := found.ID == "finish" || (len(checkpoints) > 0 && found.Sequence == checkpoints[len(checkpoints)-1].Sequence)
	if isFinish && status == "completed" {
		_ = service.CompleteEvent()
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"checkpointId": checkpointID,
		"status":       status,
	})
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
	if bibNumber == "" && participantID != "" {
		participant, found := participantByID(service.Participants(), participantID)
		if !found {
			writeProblem(w, http.StatusNotFound, "selected runner was not found")
			return
		}
		bibNumber = participant.BibNumber
	}
	// No checkpoint chosen means "dynamic": record the runner's next checkpoint
	// in course order, so the volunteer only has to type the bib number.
	var (
		log race.CheckpointLog
		err error
	)
	if strings.TrimSpace(input.CheckpointID) == "" {
		log, err = service.RecordNextCheckpoint(bibNumber, input.VolunteerID, time.Now().UTC())
	} else {
		log, err = service.RecordCheckpoint(bibNumber, input.CheckpointID, input.VolunteerID, time.Now().UTC())
	}
	if err != nil {
		writeProblem(w, statusForRaceError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, log)
}

func (s *Server) configureChestReader(w http.ResponseWriter, r *http.Request) {
	if !s.canManage(r) {
		writeProblem(w, http.StatusForbidden, "Only admins can configure the chest reader.")
		return
	}
	if _, ok := s.serviceForRequest(w, r); !ok {
		return
	}
	var input struct {
		URL           string  `json:"url"`
		Port          string  `json:"port"`
		Token         string  `json:"token"`
		MinConfidence float64 `json:"minConfidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	readerURL, err := chestReaderURL(input.URL, input.Port)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	token := strings.TrimSpace(input.Token)
	if token == "" {
		token = os.Getenv("CHEST_READER_TOKEN")
	}
	reader, err := chestreader.New(readerURL, token, input.MinConfidence)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	s.setChestReader(reader)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		"url":     readerURL,
		"message": "Chest reader connected.",
	})
}

type chestReaderCandidate struct {
	BibNumber     string  `json:"bibNumber"`
	Confidence    float64 `json:"confidence"`
	Text          string  `json:"text,omitempty"`
	Registered    bool    `json:"registered"`
	ParticipantID string  `json:"participantId,omitempty"`
	RunnerName    string  `json:"runnerName,omitempty"`
}

type chestReaderScanResponse struct {
	Enabled       bool                   `json:"enabled"`
	AutoSubmit    bool                   `json:"autoSubmit"`
	BibNumber     string                 `json:"bibNumber,omitempty"`
	ParticipantID string                 `json:"participantId,omitempty"`
	RunnerName    string                 `json:"runnerName,omitempty"`
	Confidence    float64                `json:"confidence,omitempty"`
	Candidates    []chestReaderCandidate `json:"candidates"`
	Message       string                 `json:"message"`
}

func (s *Server) scanChestNumber(w http.ResponseWriter, r *http.Request) {
	reader := s.currentChestReader()
	if reader == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Chest reader is not configured.")
		return
	}
	service, ok := s.serviceForRequest(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 6<<20)
	if err := r.ParseMultipartForm(6 << 20); err != nil {
		writeProblem(w, http.StatusBadRequest, "Image upload is required.")
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Image upload is required.")
		return
	}
	defer file.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	contentType := ""
	if header != nil {
		contentType = header.Header.Get("Content-Type")
	}
	result, err := reader.Read(ctx, header.Filename, contentType, file)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, err.Error())
		return
	}

	candidates := registeredChestReaderCandidates(result, service.Participants())
	trusted := trustedChestReaderCandidate(candidates, reader.MinConfidence())
	response := chestReaderScanResponse{
		Enabled:    true,
		Candidates: candidates,
		Message:    "No registered runner matched this scan.",
	}
	if trusted != nil {
		response.AutoSubmit = true
		response.BibNumber = trusted.BibNumber
		response.ParticipantID = trusted.ParticipantID
		response.RunnerName = trusted.RunnerName
		response.Confidence = trusted.Confidence
		response.Message = trusted.BibNumber + " matched " + trusted.RunnerName + "."
	} else if len(candidates) > 0 {
		response.Message = "Select a candidate or type the chest number."
	}
	writeJSON(w, http.StatusOK, response)
}

func participantByID(participants []race.Participant, id string) (race.Participant, bool) {
	for _, participant := range participants {
		if participant.ID == id {
			return participant, true
		}
	}
	return race.Participant{}, false
}

func (s *Server) currentChestReader() *chestreader.Client {
	s.chestReaderMu.RLock()
	defer s.chestReaderMu.RUnlock()
	return s.chestReader
}

func (s *Server) setChestReader(reader *chestreader.Client) {
	s.chestReaderMu.Lock()
	defer s.chestReaderMu.Unlock()
	s.chestReader = reader
}

func chestReaderURL(rawURL string, rawPort string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL != "" {
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			return "", errors.New("OCR URL must start with http:// or https://")
		}
		return rawURL, nil
	}
	port := strings.TrimSpace(rawPort)
	if port == "" {
		port = "8096"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number <= 0 || number > 65535 {
		return "", errors.New("OCR port must be between 1 and 65535")
	}
	return fmt.Sprintf("http://127.0.0.1:%d/read", number), nil
}

func registeredChestReaderCandidates(result chestreader.Result, participants []race.Participant) []chestReaderCandidate {
	participantsByBib := make(map[string]race.Participant, len(participants))
	for _, participant := range participants {
		participantsByBib[race.NormalizeBib(participant.BibNumber)] = participant
	}

	rawCandidates := append([]chestreader.Candidate(nil), result.Candidates...)
	if result.NormalizedBib != "" {
		rawCandidates = append(rawCandidates, chestreader.Candidate{
			BibNumber:  result.NormalizedBib,
			Confidence: result.Confidence,
			Text:       result.Text,
		})
	}

	merged := make(map[string]chestReaderCandidate, len(rawCandidates))
	for _, raw := range rawCandidates {
		bibNumber := race.NormalizeBib(raw.BibNumber)
		if bibNumber == "" {
			continue
		}
		candidate := chestReaderCandidate{
			BibNumber:  bibNumber,
			Confidence: raw.Confidence,
			Text:       raw.Text,
		}
		if participant, ok := participantsByBib[bibNumber]; ok {
			candidate.Registered = true
			candidate.ParticipantID = participant.ID
			candidate.RunnerName = participant.Name
		}
		existing, exists := merged[bibNumber]
		if !exists || candidate.Confidence > existing.Confidence {
			merged[bibNumber] = candidate
		}
	}

	candidates := make([]chestReaderCandidate, 0, len(merged))
	for _, candidate := range merged {
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Registered != candidates[j].Registered {
			return candidates[i].Registered
		}
		return candidates[i].Confidence > candidates[j].Confidence
	})
	return candidates
}

func trustedChestReaderCandidate(candidates []chestReaderCandidate, minConfidence float64) *chestReaderCandidate {
	var trusted *chestReaderCandidate
	for i := range candidates {
		if !candidates[i].Registered || candidates[i].Confidence < minConfidence {
			continue
		}
		if trusted != nil {
			return nil
		}
		trusted = &candidates[i]
	}
	return trusted
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
	case errors.Is(err, race.ErrDuplicateEntry), errors.Is(err, race.ErrOutOfOrderEntry), errors.Is(err, race.ErrInvalidParticipant), errors.Is(err, race.ErrRaceComplete), errors.Is(err, race.ErrBibLocked):
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

// marathonBibTaken reports whether the given bib is already registered in another
// race of the same marathon, so one bib maps to one runner across 5/10/21 KM etc.
// Returns the conflicting race name. Ungrouped races (empty marathonID) are not
// cross-checked.
func (s *Server) marathonBibTaken(marathonID, eventID, bib string) (string, bool) {
	bib = race.NormalizeBib(bib)
	if marathonID == "" || bib == "" {
		return "", false
	}
	s.projects.mu.RLock()
	defer s.projects.mu.RUnlock()
	for _, id := range s.projects.ids {
		if id == eventID {
			continue
		}
		service := s.projects.services[id]
		if service.Event().MarathonID != marathonID {
			continue
		}
		for _, participant := range service.Participants() {
			if race.NormalizeBib(participant.BibNumber) == bib {
				return service.Event().Name, true
			}
		}
	}
	return "", false
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

func (s *Server) hasProject(id string) bool {
	s.projects.mu.RLock()
	defer s.projects.mu.RUnlock()
	_, ok := s.projects.services[id]
	return ok
}

func (s *Server) removeProject(id string) (string, bool) {
	s.projects.mu.Lock()
	defer s.projects.mu.Unlock()
	if _, exists := s.projects.services[id]; !exists {
		return "", false
	}
	delete(s.projects.services, id)
	for i, existingID := range s.projects.ids {
		if existingID == id {
			s.projects.ids = append(s.projects.ids[:i], s.projects.ids[i+1:]...)
			break
		}
	}
	if s.projects.activeID != id {
		return s.dashboardURLLocked(), true
	}
	s.projects.activeID = ""
	s.service = nil
	if len(s.projects.ids) > 0 {
		nextID := s.projects.ids[0]
		s.projects.activeID = nextID
		s.service = s.projects.services[nextID]
	}
	return s.dashboardURLLocked(), true
}

func (s *Server) dashboardURLLocked() string {
	return "/"
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
		leaderboardURL := "/events/" + event.ID + "/leaderboard"
		if event.ID == s.projects.activeID {
			raceURL = "/race"
			dashboardURL = "/"
			leaderboardURL = "/leaderboard"
		}
		marathonID := event.MarathonID
		if marathonID == "" {
			marathonID = event.ID
		}
		marathonName := event.MarathonName
		if marathonName == "" {
			marathonName = event.Name
		}
		summaries = append(summaries, projectSummary{
			ID:             event.ID,
			Name:           event.Name,
			Location:       event.Location,
			Active:         event.ID == activeID,
			DashboardURL:   dashboardURL,
			RaceURL:        raceURL,
			LeaderboardURL: leaderboardURL,
			MarathonID:     marathonID,
			MarathonName:   marathonName,
		})
	}
	return summaries
}

func (s *Server) basePathFor(eventID string) string {
	s.projects.mu.RLock()
	defer s.projects.mu.RUnlock()
	return s.basePathForLocked(eventID)
}

func (s *Server) basePathForLocked(eventID string) string {
	if eventID == s.projects.activeID {
		return ""
	}
	return "/events/" + eventID
}

// isGuestCertificatePath reports whether path is one of the certificate
// routes that guests are permitted to access.
func isGuestCertificatePath(path string) bool {
	// /runners/{bib}/certificate
	if strings.HasSuffix(path, "/certificate") {
		return true
	}
	return false
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.authManager == nil {
		return true
	}
	if r.URL.Path == "/login" || r.URL.Path == "/guest-login" || strings.HasPrefix(r.URL.Path, "/static/") {
		return true
	}
	user, ok := s.authenticatedUser(r)
	if ok {
		if user.Role == auth.RoleGuest {
			// Guests may only view certificate pages (GET only).
			if r.Method == http.MethodGet && isGuestCertificatePath(r.URL.Path) {
				return true
			}
			// Redirect to the guest info page so they understand their access level.
			http.Redirect(w, r, "/guest-login", http.StatusSeeOther)
			return false
		}
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
	return ok && (user.Role == auth.RoleAdmin || user.Role == auth.RoleVolunteer)
}

func (s *Server) authenticatedUser(r *http.Request) (auth.User, bool) {
	if s.authManager == nil {
		return auth.User{}, false
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return auth.User{}, false
	}
	now := time.Now().UTC()
	s.sessionMu.Lock()
	record, ok := s.sessions[cookie.Value]
	if ok && !record.ExpiresAt.IsZero() && now.After(record.ExpiresAt) {
		delete(s.sessions, cookie.Value)
		_ = s.saveSessionsLocked()
		ok = false
	}
	s.sessionMu.Unlock()
	if !ok {
		return auth.User{}, false
	}
	// Guest sessions are stored with a special sentinel username.
	if record.Username == "__guest__" {
		return auth.User{Username: "guest", Role: auth.RoleGuest}, true
	}
	return s.authManager.User(record.Username)
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

func (s *Server) loadSessions() {
	if s.sessionPath == "" {
		return
	}
	data, err := os.ReadFile(s.sessionPath)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	loaded := make(map[string]sessionRecord)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != 3 {
			continue
		}
		token := strings.TrimSpace(parts[0])
		username := strings.ToLower(strings.TrimSpace(parts[1]))
		expiresUnix, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		if err != nil || token == "" || username == "" {
			continue
		}
		expiresAt := time.Unix(expiresUnix, 0).UTC()
		if !expiresAt.After(now) {
			continue
		}
		loaded[token] = sessionRecord{Username: username, ExpiresAt: expiresAt}
	}
	if len(loaded) == 0 {
		return
	}
	s.sessionMu.Lock()
	s.sessions = loaded
	s.sessionMu.Unlock()
}

func (s *Server) saveSessionsLocked() error {
	if s.sessionPath == "" {
		return nil
	}
	var builder strings.Builder
	builder.WriteString("# token:username:expires_unix\n")
	tokens := make([]string, 0, len(s.sessions))
	for token := range s.sessions {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	for _, token := range tokens {
		record := s.sessions[token]
		if token == "" || record.Username == "" {
			continue
		}
		builder.WriteString(token)
		builder.WriteString(":")
		builder.WriteString(record.Username)
		builder.WriteString(":")
		builder.WriteString(strconv.FormatInt(record.ExpiresAt.Unix(), 10))
		builder.WriteString("\n")
	}
	return os.WriteFile(s.sessionPath, []byte(builder.String()), 0600)
}

func filterCategories(categories []string) []string {
	seen := make(map[string]bool, len(categories))
	result := make([]string, 0, len(categories))
	for _, c := range categories {
		c = strings.TrimSpace(c)
		if c != "" && !seen[c] {
			seen[c] = true
			result = append(result, c)
		}
	}
	return result
}

func defaultCategories(distanceKM int) []string {
	switch {
	case distanceKM >= 42:
		return []string{"5 KM", "11 KM", "21 KM", "42 KM"}
	case distanceKM >= 21:
		return []string{"5 KM", "11 KM", "21 KM"}
	case distanceKM >= 11:
		return []string{"5 KM", "11 KM"}
	default:
		return []string{fmt.Sprintf("%d KM", distanceKM)}
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
