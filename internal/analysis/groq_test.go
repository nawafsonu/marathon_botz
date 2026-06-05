package analysis

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
