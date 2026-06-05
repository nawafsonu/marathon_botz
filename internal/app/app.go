package app

import (
	"bufio"
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"marathon/internal/analysis"
	"marathon/internal/auth"
	mongostore "marathon/internal/persistence/mongo"
	"marathon/internal/race"
	"marathon/internal/web"
)

func Run() {
	loadDotEnv(".env")
	server, disconnect := buildServer()
	defer disconnect()

	addr := ":" + env("PORT", "8080")

	log.Printf("Marathon Tracker running at http://localhost%s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatal(err)
	}
}

func buildServer() (*web.Server, func()) {
	duplicateWindow := 10 * time.Minute
	authManager, err := auth.NewManager(env("LOGIN_CREDENTIALS_FILE", "logincred.txt"))
	if err != nil {
		log.Printf("Local login credentials unavailable; auth disabled: %v", err)
	}
	analyzer, err := analysis.NewClient(os.Getenv("GROQ_API_KEY"), env("GROQ_MODEL", "openai/gpt-oss-120b"))
	if err != nil {
		log.Printf("Groq analysis unavailable: %v", err)
	}
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		log.Fatal("MONGODB_URI is required; refusing to start without MongoDB persistence")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := mongostore.Connect(ctx, uri, env("MONGODB_DATABASE", databaseFromURI(uri, "marathon_tracker")))
	if err != nil {
		log.Fatalf("MongoDB persistence unavailable; refusing to start in memory: %v", err)
	}

	states, err := store.LoadAll(ctx)
	if err != nil {
		log.Fatalf("MongoDB state load failed; refusing to start in memory: %v", err)
	}

	if len(states) == 0 {
		log.Printf("No Marathon Tracker projects found; starting empty")
		return web.NewServer(nil, web.WithProjectStore(store), web.WithAuthManager(authManager), web.WithAnalyzer(analyzer)), func() { _ = store.Disconnect(context.Background()) }
	}

	services := make([]*race.Service, 0, len(states))
	for _, state := range states {
		service := race.NewServiceFromState(state, duplicateWindow)
		if err := service.UseStore(store); err != nil {
			log.Printf("MongoDB save hook failed for %s: %v", service.Event().ID, err)
		}
		services = append(services, service)
	}
	log.Printf("Loaded %d Marathon Tracker project(s) from MongoDB", len(services))
	return web.NewServer(services[0], web.WithProjectStore(store), web.WithProjectServices(services), web.WithAuthManager(authManager), web.WithAnalyzer(analyzer)), func() { _ = store.Disconnect(context.Background()) }
}

func event() race.Event {
	return race.Event{
		ID:          "koch-2026",
		Name:        "Kochi Marathon 2026",
		Description: "Live marathon command center powered by professional race analytics.",
		Date:        time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
		StartTime:   time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC),
		Location:    "Kochi, Kerala",
		DistanceKM:  42,
		Status:      race.EventStatusActive,
	}
}

func checkpoints() []race.Checkpoint {
	return []race.Checkpoint{
		{ID: "start", Name: "Start", Sequence: 1, DistanceKM: 0},
		{ID: "cp1", Name: "CP1", Sequence: 2, DistanceKM: 5},
		{ID: "cp2", Name: "CP2", Sequence: 3, DistanceKM: 10},
		{ID: "cp3", Name: "CP3", Sequence: 4, DistanceKM: 20},
		{ID: "finish", Name: "Finish", Sequence: 5, DistanceKM: 42},
	}
}

func seedDemoRace(service *race.Service) {
	start := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)
	runners := []struct {
		name   string
		phone  string
		offset []time.Duration
	}{
		{"Dev Rao", "+91 90000 10001", []time.Duration{0, 20 * time.Minute, 47 * time.Minute, 76 * time.Minute, 2*time.Hour + 42*time.Minute}},
		{"Maya Iyer", "+91 90000 10002", []time.Duration{0, 22 * time.Minute, 50 * time.Minute, 82 * time.Minute, 2*time.Hour + 51*time.Minute}},
		{"Nila Shah", "+91 90000 10003", []time.Duration{0, 19 * time.Minute, 49 * time.Minute, 83 * time.Minute}},
		{"Arjun Nair", "+91 90000 10004", []time.Duration{0, 25 * time.Minute, 56 * time.Minute}},
		{"Sara Thomas", "+91 90000 10005", []time.Duration{0, 27 * time.Minute}},
		{"Kabir Khan", "+91 90000 10006", []time.Duration{0}},
	}

	checkpointIDs := []string{"start", "cp1", "cp2", "cp3", "finish"}
	for _, runner := range runners {
		participant, err := service.RegisterParticipant(runner.name, runner.phone, "")
		if err != nil {
			log.Printf("seed participant %s: %v", runner.name, err)
			continue
		}
		for i, offset := range runner.offset {
			if i >= len(checkpointIDs) {
				break
			}
			if _, err := service.RecordCheckpoint(participant.BibNumber, checkpointIDs[i], "seed-volunteer", start.Add(offset)); err != nil {
				log.Printf("seed log %s %s: %v", participant.BibNumber, checkpointIDs[i], err)
			}
		}
	}
	if err := service.MarkDNF("BIB-006"); err != nil {
		log.Printf("seed dnf: %v", err)
	}
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func databaseFromURI(uri string, fallback string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		return fallback
	}
	database := strings.Trim(parsed.Path, "/")
	if database == "" {
		return fallback
	}
	return database
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimPrefix(strings.TrimSpace(key), "\ufeff")
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			log.Printf("Ignoring invalid .env key %q: %v", key, err)
		}
	}
}
