package chestreader

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewDisabledWithoutURL(t *testing.T) {
	if _, err := New("", "", 0.82); !errorsIs(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}

func TestReadSendsImageAndBearerToken(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		if !strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/form-data") {
			t.Fatalf("content type = %q", req.Header.Get("Content-Type"))
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		_, header, err := req.FormFile("image")
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		if got := header.Header.Get("Content-Type"); got != "image/jpeg" {
			t.Fatalf("file content type = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"text":"001","normalizedBib":"BIB-001","confidence":0.91,"candidates":[{"bibNumber":"BIB-001","confidence":0.91}],"boxes":[]}`)),
		}, nil
	})
	client, err := New("https://reader.test/read", "secret", 0.82, WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.Read(context.Background(), "frame.jpg", "image/jpeg", strings.NewReader("image-bytes"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result.NormalizedBib != "BIB-001" || result.Candidates[0].BibNumber != "BIB-001" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestReadRejectsOversizedImage(t *testing.T) {
	client, err := New("https://reader.test/read", "", 0.82, WithMaxImageBytes(4), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("transport should not be called for oversized image")
		return nil, nil
	})}))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.Read(context.Background(), "frame.jpg", "image/jpeg", strings.NewReader("12345")); err == nil {
		t.Fatal("oversized image was accepted")
	}
}

func errorsIs(err error, target error) bool {
	return err != nil && target != nil && strings.Contains(err.Error(), target.Error())
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
