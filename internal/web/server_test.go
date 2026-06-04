package web

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marathon/internal/race"
)

func TestStateEndpointReturnsLiveSummary(t *testing.T) {
	svc := testService(t)
	handler := NewServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	var body race.Snapshot
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if body.Event.Name != "Kochi Marathon 2026" {
		t.Fatalf("event name = %q", body.Event.Name)
	}
	if body.Summary.TotalParticipants != 2 || body.Summary.Active != 2 {
		t.Fatalf("unexpected summary: %+v", body.Summary)
	}
}

func TestRegisterParticipantEndpointCreatesBib(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	handler := NewServer(svc)

	payload := []byte(`{"name":"Priya Raman","phoneNumber":"+91 99999 11111","notes":"10K upgrade"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/participants", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", res.Code, res.Body.String())
	}
	var participant race.Participant
	if err := json.NewDecoder(res.Body).Decode(&participant); err != nil {
		t.Fatalf("decode participant: %v", err)
	}
	if participant.BibNumber != "BIB-001" {
		t.Fatalf("bib = %s, want BIB-001", participant.BibNumber)
	}
}

func TestCheckpointEndpointRejectsOutOfOrderEntry(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	participant, err := svc.RegisterParticipant("Priya Raman", "+91 99999 11111", "")
	if err != nil {
		t.Fatalf("register participant: %v", err)
	}
	handler := NewServer(svc)

	payload := []byte(`{"bibNumber":"` + participant.BibNumber + `","checkpointId":"cp2","volunteerId":"vol-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/checkpoint-logs", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "previous checkpoint") {
		t.Fatalf("body did not explain validation error: %s", res.Body.String())
	}
}

func TestFinalCSVExportContainsRankedRunners(t *testing.T) {
	svc := testService(t)
	handler := NewServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/reports/final.csv", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	reader := csv.NewReader(strings.NewReader(res.Body.String()))
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if got := strings.Join(rows[0], ","); got != "Rank,Bib,Name,Status,Latest Checkpoint,Finish Time,Race Time,Gap" {
		t.Fatalf("csv header = %q", got)
	}
	if rows[1][1] != "BIB-002" {
		t.Fatalf("first ranked bib = %s, want BIB-002", rows[1][1])
	}
}

func testService(t *testing.T) *race.Service {
	t.Helper()
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	start := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)
	slow, err := svc.RegisterParticipant("Maya Iyer", "+91 90000 10001", "")
	if err != nil {
		t.Fatalf("register slow: %v", err)
	}
	fast, err := svc.RegisterParticipant("Dev Rao", "+91 90000 10002", "")
	if err != nil {
		t.Fatalf("register fast: %v", err)
	}
	for _, step := range []struct {
		bib        string
		checkpoint string
		offset     time.Duration
	}{
		{slow.BibNumber, "start", 0},
		{slow.BibNumber, "cp1", 25 * time.Minute},
		{fast.BibNumber, "start", 0},
		{fast.BibNumber, "cp1", 20 * time.Minute},
	} {
		if _, err := svc.RecordCheckpoint(step.bib, step.checkpoint, "vol-1", start.Add(step.offset)); err != nil {
			t.Fatalf("record %s %s: %v", step.bib, step.checkpoint, err)
		}
	}
	return svc
}

func testEvent() race.Event {
	return race.Event{
		ID:          "event-1",
		Name:        "Kochi Marathon 2026",
		Description: "City marathon command center",
		Date:        time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
		StartTime:   time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC),
		Location:    "Kochi, Kerala",
		DistanceKM:  42,
		Status:      race.EventStatusActive,
	}
}

func testCheckpoints() []race.Checkpoint {
	return []race.Checkpoint{
		{ID: "start", Name: "Start", Sequence: 1, DistanceKM: 0},
		{ID: "cp1", Name: "CP1", Sequence: 2, DistanceKM: 5},
		{ID: "cp2", Name: "CP2", Sequence: 3, DistanceKM: 10},
		{ID: "finish", Name: "Finish", Sequence: 4, DistanceKM: 42},
	}
}
