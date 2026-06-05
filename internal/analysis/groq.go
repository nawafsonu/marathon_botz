package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"marathon/internal/race"
)

const (
	defaultEndpoint = "https://api.groq.com/openai/v1/chat/completions"
	defaultModel    = "openai/gpt-oss-120b"
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

func (c *Client) AnalyzeRace(ctx context.Context, snapshot race.Snapshot) (string, error) {
	payload := struct {
		Event       race.Event              `json:"event"`
		Summary     race.Summary            `json:"summary"`
		Checkpoints []race.Checkpoint        `json:"checkpoints"`
		Leaderboard []race.LeaderboardEntry  `json:"leaderboard"`
		LiveFeed    []race.CheckpointLog     `json:"liveFeed"`
		Participants []race.Participant      `json:"participants"`
	}{
		Event:        snapshot.Event,
		Summary:      snapshot.Summary,
		Checkpoints:  snapshot.Checkpoints,
		Leaderboard:  firstN(snapshot.Leaderboard, 12),
		LiveFeed:     firstN(snapshot.LiveFeed, 12),
		Participants: firstN(snapshot.Participants, 20),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return c.complete(ctx, "You are a marathon operations analyst. Give concise, practical race-control insight. Focus on current race health, checkpoint flow, anomalies, and next actions. Avoid medical advice.", string(data))
}

func (c *Client) AnalyzeRunner(ctx context.Context, event race.Event, profile race.RunnerProfile) (string, error) {
	payload := struct {
		Event   race.Event         `json:"event"`
		Profile race.RunnerProfile `json:"profile"`
	}{
		Event:   event,
		Profile: profile,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return c.complete(ctx, "You are a marathon runner performance analyst. Give concise checkpoint-based runner analysis using only the supplied timing data. Mention status, pacing signals, gaps, and what race staff should watch. Avoid medical advice.", string(data))
}

func (c *Client) complete(ctx context.Context, systemPrompt string, userPrompt string) (string, error) {
	requestBody := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.2,
		MaxTokens:   450,
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

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
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
