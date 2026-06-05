package chestreader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

const defaultMaxImageBytes int64 = 5 << 20

var ErrDisabled = errors.New("chest reader is not configured")

type Client struct {
	url           string
	token         string
	minConfidence float64
	maxImageBytes int64
	httpClient    *http.Client
}

type Option func(*Client)

type Result struct {
	Text          string      `json:"text"`
	NormalizedBib string      `json:"normalizedBib"`
	Confidence    float64     `json:"confidence"`
	Candidates    []Candidate `json:"candidates"`
	Boxes         []Box       `json:"boxes"`
}

type Candidate struct {
	BibNumber  string  `json:"bibNumber"`
	Confidence float64 `json:"confidence"`
	Text       string  `json:"text,omitempty"`
}

type Box struct {
	X          int     `json:"x"`
	Y          int     `json:"y"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Confidence float64 `json:"confidence"`
	Label      string  `json:"label"`
}

func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

func WithMaxImageBytes(limit int64) Option {
	return func(c *Client) {
		if limit > 0 {
			c.maxImageBytes = limit
		}
	}
}

func New(rawURL string, token string, minConfidence float64, options ...Option) (*Client, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, ErrDisabled
	}
	if minConfidence <= 0 || minConfidence > 1 {
		minConfidence = 0.82
	}
	client := &Client{
		url:           rawURL,
		token:         strings.TrimSpace(token),
		minConfidence: minConfidence,
		maxImageBytes: defaultMaxImageBytes,
		httpClient: &http.Client{
			Timeout: 12 * time.Second,
		},
	}
	for _, option := range options {
		option(client)
	}
	return client, nil
}

func (c *Client) MinConfidence() float64 {
	if c == nil {
		return 0.82
	}
	return c.minConfidence
}

func (c *Client) Read(ctx context.Context, filename string, contentType string, reader io.Reader) (Result, error) {
	if c == nil || c.url == "" {
		return Result{}, ErrDisabled
	}
	data, err := readLimited(reader, c.maxImageBytes)
	if err != nil {
		return Result{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if strings.TrimSpace(contentType) == "" {
		contentType = "image/jpeg"
	}
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="image"; filename="`+escapeMultipartFilename(safeFilename(filename))+`"`)
	partHeader.Set("Content-Type", contentType)
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return Result{}, err
	}
	if _, err := part.Write(data); err != nil {
		return Result{}, err
	}
	if err := writer.Close(); err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, &body)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer res.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Result{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Result{}, fmt.Errorf("chest reader failed with status %d: %s", res.StatusCode, serviceError(responseBody))
	}
	var result Result
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("image is required")
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("image is too large; max %d bytes", limit)
	}
	if len(data) == 0 {
		return nil, errors.New("image is required")
	}
	return data, nil
}

func safeFilename(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return "frame.jpg"
	}
	return filename
}

func escapeMultipartFilename(filename string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(filename)
}

func serviceError(body []byte) string {
	var parsed struct {
		Detail string `json:"detail"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if strings.TrimSpace(parsed.Detail) != "" {
			return strings.TrimSpace(parsed.Detail)
		}
		if strings.TrimSpace(parsed.Error) != "" {
			return strings.TrimSpace(parsed.Error)
		}
	}
	return strings.TrimSpace(string(body))
}
