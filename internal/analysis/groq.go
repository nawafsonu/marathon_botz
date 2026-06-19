package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"marathon/internal/race"
)

const (
	defaultEndpoint = "https://api.groq.com/openai/v1/chat/completions"
	defaultModel    = "llama-3.3-70b-specdec"
)

type Client struct {
	apiKey     string
	model      string
	endpoint   string
	httpClient *http.Client
}

type Option func(*Client)

func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

func WithEndpoint(endpoint string) Option {
	return func(c *Client) {
		if strings.TrimSpace(endpoint) != "" {
			c.endpoint = strings.TrimSpace(endpoint)
		}
	}
}

func NewClient(apiKey string, model string, options ...Option) (*Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("GROQ_API_KEY is required")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultModel
	}
	client := &Client{
		apiKey:   apiKey,
		model:    model,
		endpoint: defaultEndpoint,
		httpClient: &http.Client{
			Timeout: 25 * time.Second,
		},
	}
	for _, option := range options {
		option(client)
	}
	return client, nil
}

func raceAnalysisSystemPrompt() string {
	return strings.Join([]string{
		"You are a Senior Marathon Operations Analyst.",
		"Use the supplied JSON data to perform a deep, detailed, and comprehensive analysis of the current race state.",
		"Specifically, cover:",
		"1. Overall Event Status: summarize total registered runners, finished runners, active runners, and DNF counts.",
		"2. Leaderboard Dynamics: analyze top performers, category-specific leaders, close timing gaps, and intense placement battles.",
		"3. Live Feed Trends: summarize notable recent events and runner progress patterns.",
		"4. Operational Highlights: flag any potential timing anomalies, runners with unusually high pace/speed changes, or other concerns for the race coordinators.",
		"Avoid giving any hydration, nutrition, medical, coaching, or training advice.",
		"Structure your response professionally with clear sections, using Markdown formatting for readability.",
	}, " ")
}

func (c *Client) AnalyzeRace(ctx context.Context, snapshot race.Snapshot) (string, error) {
	payload := raceAnalysisPayload(snapshot)
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return c.completeWithLimit(ctx, raceAnalysisSystemPrompt(), string(data), 1024)
}

func (c *Client) AnalyzeRunner(ctx context.Context, event race.Event, profile race.RunnerProfile) (string, error) {
	payload := runnerAnalysisPayload(event, profile)
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return c.completeWithLimit(ctx, runnerAnalysisSystemPrompt(), string(data), 1024)
}

func raceAnalysisPayload(snapshot race.Snapshot) map[string]any {
	checkpoints := make([]map[string]any, 0, len(snapshot.Checkpoints))
	for _, checkpoint := range snapshot.Checkpoints {
		checkpoints = append(checkpoints, map[string]any{
			"n":   checkpoint.Name,
			"seq": checkpoint.Sequence,
			"km":  checkpoint.DistanceKM,
		})
	}
	leaderboard := make([]map[string]any, 0, len(firstN(snapshot.Leaderboard, 10)))
	for _, entry := range firstN(snapshot.Leaderboard, 10) {
		leaderboard = append(leaderboard, map[string]any{
			"bib":    entry.BibNumber,
			"name":   entry.RunnerName,
			"rank":   entry.Rank,
			"status": entry.Status,
			"cp":     entry.LatestCheckpoint,
			"time":   entry.RaceTime,
			"gap":    entry.Gap,
		})
	}
	categoryLeaderboards := make([]map[string]any, 0, len(snapshot.CategoryLeaderboards))
	for _, catBoard := range snapshot.CategoryLeaderboards {
		entries := make([]map[string]any, 0, len(firstN(catBoard.Entries, 5)))
		for _, entry := range firstN(catBoard.Entries, 5) {
			entries = append(entries, map[string]any{
				"bib":    entry.BibNumber,
				"name":   entry.RunnerName,
				"rank":   entry.Rank,
				"status": entry.Status,
				"cp":     entry.LatestCheckpoint,
				"time":   entry.RaceTime,
				"gap":    entry.Gap,
			})
		}
		categoryLeaderboards = append(categoryLeaderboards, map[string]any{
			"category": catBoard.Category,
			"entries":  entries,
		})
	}
	feed := make([]map[string]any, 0, len(firstN(snapshot.LiveFeed, 8)))
	for _, log := range firstN(snapshot.LiveFeed, 8) {
		feed = append(feed, map[string]any{
			"bib": log.Participant.BibNumber,
			"cp":  log.Checkpoint.Name,
			"ts":  log.Timestamp.Format(time.RFC3339),
		})
	}
	return map[string]any{
		"event": map[string]any{
			"name":       snapshot.Event.Name,
			"distanceKm": snapshot.Event.DistanceKM,
			"status":     snapshot.Event.Status,
		},
		"summary":              snapshot.Summary,
		"checkpoints":          checkpoints,
		"leaders":              leaderboard,
		"categoryLeaderboards": categoryLeaderboards,
		"feed":                 feed,
	}
}

func runnerAnalysisSystemPrompt() string {
	return strings.Join([]string{
		"Runner performance analyst for marathon ops.",
		"Use supplied runner/timing/checkpoint/segment/gap data only.",
		"Analyze checkpoint-to-checkpoint speed and pace using checkpointSpeedSegments first.",
		"Call out fastest, slowest, and abnormal checkpoint segments with segment names.",
		"performance must be an object with checkpointSpeedSummary, fastestSegment, slowestSegment, and segmentAnalysis array.",
		"Each segmentAnalysis item must include from, to, distanceKm, duration, pace, speedKmh, and anomaly.",
		"checkpointInsight must compare every checkpointSpeedSegments item by pace/speed and name the exact segment.",
		"Never give hydration, nutrition, injury, medical, training, recovery, motivational, or coaching advice.",
		"nextAction and staffNotes must be timing-ops actions only: verify checkpoint timestamp, device clock, duplicate scan, missed scan, or course distance data.",
		"Return only valid JSON: summary, performance, checkpointInsight, gapInsight, riskLevel, nextAction, staffNotes.",
		"performance should summarize segment speed/pace, not coaching. riskLevel: low, watch, or urgent. staffNotes: max 3 short timing audit notes.",
	}, " ")
}

func runnerAnalysisPayload(event race.Event, profile race.RunnerProfile) map[string]any {
	timelineLogs := lastN(profile.Timeline, 12)
	timeline := make([]map[string]any, 0, len(timelineLogs))
	for _, log := range timelineLogs {
		timeline = append(timeline, map[string]any{
			"cp":  log.Checkpoint.Name,
			"seq": log.Checkpoint.Sequence,
			"km":  log.Checkpoint.DistanceKM,
			"ts":  log.Timestamp.Format(time.RFC3339),
		})
	}
	segmentRows := lastN(profile.Segments, 10)
	segments := make([]map[string]any, 0, len(segmentRows))
	for _, segment := range segmentRows {
		segments = append(segments, map[string]any{
			"from": segment.From,
			"to":   segment.To,
			"dur":  segment.Duration,
		})
	}
	speedSegments := checkpointSpeedSegments(timelineLogs, 10)
	return map[string]any{
		"marathon": map[string]any{
			"name":       event.Name,
			"location":   event.Location,
			"distanceKm": event.DistanceKM,
			"status":     event.Status,
		},
		"runner": map[string]any{
			"name":   profile.Participant.Name,
			"bib":    profile.Participant.BibNumber,
			"status": profile.Participant.Status,
		},
		"standing": map[string]any{
			"rank":             profile.Summary.Rank,
			"latestCheckpoint": profile.Summary.LatestCheckpoint,
			"latestSequence":   profile.Summary.LatestSequence,
			"finishTime":       profile.Summary.FinishTime,
			"raceTime":         profile.Summary.RaceTime,
			"gap":              profile.Summary.Gap,
		},
		"checkpointTimeline":       timeline,
		"segmentDurations":         segments,
		"checkpointSpeedSegments":  speedSegments,
		"analysisInstructionFocus": "checkpoint-to-checkpoint speed, pace, ranking gap, and timing anomalies only",
	}
}

func checkpointSpeedSegments(timeline []race.CheckpointLog, limit int) []map[string]any {
	if len(timeline) < 2 {
		return nil
	}
	segments := make([]map[string]any, 0, len(timeline)-1)
	for i := 1; i < len(timeline); i++ {
		from := timeline[i-1]
		to := timeline[i]
		distanceKM := to.Checkpoint.DistanceKM - from.Checkpoint.DistanceKM
		duration := to.Timestamp.Sub(from.Timestamp)
		durationSeconds := int(duration.Round(time.Second).Seconds())
		segment := map[string]any{
			"from":            from.Checkpoint.Name,
			"to":              to.Checkpoint.Name,
			"distanceKm":      roundFloat(distanceKM, 2),
			"durationSeconds": durationSeconds,
			"duration":        formatAnalysisDuration(duration),
		}
		if distanceKM > 0 && durationSeconds > 0 {
			paceSeconds := int(math.Round(float64(durationSeconds) / distanceKM))
			segment["paceSecPerKm"] = paceSeconds
			segment["pace"] = formatAnalysisPace(paceSeconds)
			segment["speedKmh"] = roundFloat(distanceKM/duration.Hours(), 1)
		} else {
			segment["pace"] = "unavailable"
			segment["speedKmh"] = 0
		}
		segments = append(segments, segment)
	}
	return lastN(segments, limit)
}

func formatAnalysisDuration(duration time.Duration) string {
	if duration < 0 {
		duration = -duration
	}
	totalSeconds := int(duration.Round(time.Second).Seconds())
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %02dm %02ds", hours, minutes, seconds)
	}
	return fmt.Sprintf("%dm %02ds", minutes, seconds)
}

func formatAnalysisPace(secondsPerKM int) string {
	if secondsPerKM < 0 {
		secondsPerKM = -secondsPerKM
	}
	minutes := secondsPerKM / 60
	seconds := secondsPerKM % 60
	return fmt.Sprintf("%d:%02d/km", minutes, seconds)
}

func roundFloat(value float64, places int) float64 {
	scale := math.Pow10(places)
	return math.Round(value*scale) / scale
}

func (c *Client) complete(ctx context.Context, systemPrompt string, userPrompt string) (string, error) {
	return c.completeWithLimit(ctx, systemPrompt, userPrompt, 450)
}

func (c *Client) completeWithLimit(ctx context.Context, systemPrompt string, userPrompt string, maxTokens int) (string, error) {
	requestBody := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.2,
		MaxTokens:   maxTokens,
	}
	if isGPTOSSModel(c.model) {
		includeReasoning := false
		requestBody.IncludeReasoning = &includeReasoning
		requestBody.ReasoningEffort = "low"
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("Groq analysis failed with status %d: %s", res.StatusCode, groqErrorMessage(responseBody))
	}
	var response chatResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return "", err
	}
	if len(response.Choices) == 0 {
		return "", errors.New("Groq returned no analysis")
	}
	content := strings.TrimSpace(response.Choices[0].Message.Content)
	if content == "" {
		return "", errors.New("Groq returned empty analysis")
	}
	return content, nil
}

func groqErrorMessage(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return strings.TrimSpace(parsed.Error.Message)
	}
	return strings.TrimSpace(string(body))
}

func firstN[T any](items []T, limit int) []T {
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}

func lastN[T any](items []T, limit int) []T {
	if len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func isGPTOSSModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "openai/gpt-oss-")
}

type chatRequest struct {
	Model            string        `json:"model"`
	Messages         []chatMessage `json:"messages"`
	Temperature      float64       `json:"temperature"`
	MaxTokens        int           `json:"max_tokens"`
	ReasoningEffort  string        `json:"reasoning_effort,omitempty"`
	IncludeReasoning *bool         `json:"include_reasoning,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}
