package analysis

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"marathon/internal/race"
)

func TestAnalyzeRaceUsesConfiguredGroqModel(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization header = %q", got)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		text := string(body)
		if !strings.Contains(text, `"model":"openai/gpt-oss-120b"`) {
			t.Fatalf("request did not use gpt-oss model: %s", text)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"Race is stable."}}]}`)),
		}, nil
	})
	client, err := NewClient("test-key", "", WithHTTPClient(&http.Client{Transport: transport}), WithEndpoint("https://example.test/chat"))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	text, err := client.AnalyzeRace(context.Background(), race.Snapshot{
		Event: race.Event{Name: "Mumbai Marathon"},
		Summary: race.Summary{
			TotalParticipants: 10,
		},
	})
	if err != nil {
		t.Fatalf("analyze race: %v", err)
	}
	if text != "Race is stable." {
		t.Fatalf("analysis = %q", text)
	}
}

func TestAnalyzeRunnerSendsSpecificRunnerDataAndStructuredPrompt(t *testing.T) {
	var captured chatRequest
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"{\"summary\":\"steady\",\"performance\":\"consistent\",\"checkpointInsight\":\"CP1 clean\",\"gapInsight\":\"+2m @ CP1\",\"riskLevel\":\"low\",\"nextAction\":\"monitor\",\"staffNotes\":[\"watch CP2\"]}"}}]}`)),
		}, nil
	})
	client, err := NewClient("test-key", "", WithHTTPClient(&http.Client{Transport: transport}), WithEndpoint("https://example.test/chat"))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	start := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)
	profile := race.RunnerProfile{
		Participant: race.Participant{ID: "runner-007", BibNumber: "BIB-007", Name: "Nawaf", Status: race.RaceStatusActive, Notes: "VIP"},
		Summary: race.LeaderboardEntry{
			Rank:             3,
			BibNumber:        "BIB-007",
			RunnerName:       "Nawaf",
			Status:           race.RaceStatusActive,
			LatestCheckpoint: "CP1",
			LatestSequence:   2,
			RaceTime:         "—",
			Gap:              "+2m 00s @ CP1",
		},
		Timeline: []race.CheckpointLog{
			{
				Checkpoint:  race.Checkpoint{ID: "cp1", Name: "CP1", Sequence: 2, DistanceKM: 5},
				Timestamp:   start.Add(25 * time.Minute),
				VolunteerID: "vol-1",
			},
		},
		Segments: []race.Segment{{From: "Start", To: "CP1", Duration: "25m 00s"}},
	}

	text, err := client.AnalyzeRunner(context.Background(), race.Event{
		ID:         "mumbai-2026",
		Name:       "Mumbai Marathon 2026",
		Location:   "Mumbai",
		DistanceKM: 42,
		StartTime:  start,
		Status:     race.EventStatusActive,
	}, profile)
	if err != nil {
		t.Fatalf("analyze runner: %v", err)
	}
	if !strings.Contains(text, `"summary":"steady"`) {
		t.Fatalf("analysis response = %q", text)
	}
	if len(captured.Messages) != 2 {
		t.Fatalf("messages = %+v, want system and user", captured.Messages)
	}
	if !strings.Contains(captured.Messages[0].Content, "Return only valid JSON") {
		t.Fatalf("system prompt is not structured: %s", captured.Messages[0].Content)
	}
	if captured.MaxTokens > 360 {
		t.Fatalf("max tokens = %d, want <= 360", captured.MaxTokens)
	}
	userPayload := captured.Messages[1].Content
	for _, required := range []string{"Mumbai Marathon 2026", "Nawaf", "BIB-007", "CP1", "segmentDurations", "+2m 00s @ CP1"} {
		if !strings.Contains(userPayload, required) {
			t.Fatalf("runner payload missing %q: %s", required, userPayload)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
