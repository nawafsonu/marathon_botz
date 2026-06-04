package main

import (
	"bufio"
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	mongostore "marathon/internal/persistence/mongo"
	"marathon/internal/race"
	"marathon/internal/web"
)

func main() {
	loadDotEnv(".env")
	service, disconnect := buildService()
	defer disconnect()

	addr := ":" + env("PORT", "8080")
	server := web.NewServer(service)

	log.Printf("Marathon Tracker running at http://localhost%s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatal(err)
	}
}

func buildService() (*race.Service, func()) {
	duplicateWindow := 10 * time.Minute
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		service := race.NewService(event(), checkpoints(), nil, duplicateWindow)
		seedDemoRace(service)
		return service, func() {}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := mongostore.Connect(ctx, uri, env("MONGODB_DATABASE", databaseFromURI(uri, "marathon_tracker")))
	if err != nil {
		log.Printf("MongoDB persistence unavailable; using in-memory state: %v", err)
		service := race.NewService(event(), checkpoints(), nil, duplicateWindow)
		seedDemoRace(service)
		return service, func() {}
	}

	state, found, err := store.Load(ctx)
	if err != nil {
		log.Printf("MongoDB state load failed; using seeded in-memory state: %v", err)
		service := race.NewService(event(), checkpoints(), nil, duplicateWindow)
		seedDemoRace(service)
		return service, func() { _ = store.Disconnect(context.Background()) }
	}

	var service *race.Service
	if found {
		service = race.NewServiceFromState(state, duplicateWindow)
		log.Printf("Loaded Marathon Tracker state from MongoDB")
	} else {
		service = race.NewService(event(), checkpoints(), nil, duplicateWindow)
		seedDemoRace(service)
		log.Printf("Initialized Marathon Tracker seed state in MongoDB")
	}
	if err := service.UseStore(store); err != nil {
		log.Printf("MongoDB initial save failed; continuing with in-memory state: %v", err)
	}
	return service, func() { _ = store.Disconnect(context.Background()) }
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
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			log.Printf("Ignoring invalid .env key %q: %v", key, err)
		}
	}
}
