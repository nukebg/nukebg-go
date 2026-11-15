package nukebg

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient("test-key", append([]Option{WithBaseURL(srv.URL)}, opts...)...)
}

func imageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Width", "640")
	w.Header().Set("X-Height", "480")
	w.Header().Set("X-Credits-Charged", "1")
	w.Header().Set("X-Credits-Remaining", "49")
	w.Header().Set("X-Foreground", "10,20,300,400")
	w.Header().Set("X-Request-Id", "req_1")
}

func TestRemoveBackgroundMultipart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shoe.jpg")
	if err := os.WriteFile(path, []byte("JPEGDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/cutouts" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" || r.Header.Get("Idempotency-Key") == "" || !strings.HasPrefix(r.Header.Get("User-Agent"), "nukebg-go/") {
			t.Errorf("headers: %v", r.Header)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"background": "white", "size": "amazon", "crop": "true", "padding": "8.5", "format": "jpg"}
		for k, v := range want {
			if got := r.FormValue(k); got != v {
				t.Errorf("field %s = %q, want %q", k, got, v)
			}
		}
		if r.FormValue("quality") != "" || r.FormValue("width") != "" {
			t.Errorf("zero values must not be sent: quality=%q width=%q", r.FormValue("quality"), r.FormValue("width"))
		}
		f, hdr, err := r.FormFile("image_file")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		if string(b) != "JPEGDATA" || hdr.Filename != "shoe.jpg" || hdr.Header.Get("Content-Type") != "image/jpeg" {
			t.Errorf("file: %q %q %q", b, hdr.Filename, hdr.Header.Get("Content-Type"))
		}
		imageHeaders(w)
		_, _ = w.Write([]byte("PNGDATA"))
	})
	crop, pad := true, 8.5
	res, err := c.RemoveBackground(context.Background(), ImageFile(path), &RemoveBackgroundParams{Background: "white", Size: "amazon", Crop: &crop, Padding: &pad, Format: "jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != "PNGDATA" || res.Width != 640 || res.Height != 480 || res.CreditsCharged != 1 || res.CreditsRemaining == nil || *res.CreditsRemaining != 49 || res.RequestID != "req_1" {
		t.Errorf("result: %+v", res)
	}
	if res.Foreground == nil || *res.Foreground != (Box{10, 20, 300, 400}) {
		t.Errorf("foreground: %+v", res.Foreground)
	}
	out := filepath.Join(dir, "out.png")
	if err := res.Save(out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "PNGDATA" {
		t.Errorf("saved %q", b)
	}
}

func TestImageURLSendsField(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("image_url") != "https://example.com/a.jpg" {
			t.Errorf("image_url = %q", r.FormValue("image_url"))
		}
		if _, _, err := r.FormFile("image_file"); err == nil {
			t.Error("no file expected for a URL input")
		}
		imageHeaders(w)
	})
	if _, err := c.RemoveBackground(context.Background(), ImageURL("https://example.com/a.jpg"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestRetriesKeepIdempotencyKey(t *testing.T) {
	var calls atomic.Int32
	keys := make(chan string, 3)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys <- r.Header.Get("Idempotency-Key")
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow down"}}`))
			return
		}
		imageHeaders(w)
	})
	start := time.Now()
	if _, err := c.RemoveBackground(context.Background(), ImageBytes([]byte("x"), "a.png"), nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
	if time.Since(start) < 2*time.Second {
		t.Error("Retry-After was not honoured")
	}
	k1, k2, k3 := <-keys, <-keys, <-keys
	if k1 == "" || k1 != k2 || k2 != k3 {
		t.Errorf("idempotency keys differ across retries: %q %q %q", k1, k2, k3)
	}
}

func TestErrorDecodingAndNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"code":"insufficient_credits","message":"This needs 2 credits; 0 available.","request_id":"req_9"}}`))
	})
	_, err := c.RemoveBackground(context.Background(), ImageBytes([]byte("x"), ""), nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *Error, got %T %v", err, err)
	}
	if apiErr.StatusCode != 402 || apiErr.Code != "insufficient_credits" || apiErr.RequestID != "req_9" || !strings.Contains(apiErr.Error(), "insufficient_credits") {
		t.Errorf("error: %+v", apiErr)
	}
	if calls.Load() != 1 {
		t.Errorf("4xx must not be retried, calls = %d", calls.Load())
	}
}

func TestMissingKey(t *testing.T) {
	t.Setenv("NUKEBG_API_KEY", "")
	_, err := NewClient("").Account(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Errorf("err = %v", err)
	}
}

func TestEraseNeedsPromptOrMaskAndSendsMask(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		if r.URL.Path != "/v1/erase" || r.FormValue("prompt") != "the cup" {
			t.Errorf("path %s prompt %q", r.URL.Path, r.FormValue("prompt"))
		}
		if _, _, err := r.FormFile("mask_file"); err != nil {
			t.Errorf("mask_file missing: %v", err)
		}
		imageHeaders(w)
	})
	if _, err := c.Erase(context.Background(), ImageBytes([]byte("x"), "a.png"), EraseParams{}); err == nil {
		t.Error("want an error without prompt or mask")
	}
	mask := ImageBytes([]byte("m"), "mask.png")
	if _, err := c.Erase(context.Background(), ImageBytes([]byte("x"), "a.png"), EraseParams{Prompt: "the cup", Mask: &mask}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountAndJobs(t *testing.T) {
	var polls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/account":
			_, _ = w.Write([]byte(`{"account":"u1","credits":70,"balance":{"free":20,"subscription":0,"paid":50},"plan":"free","webhook_secret":"s"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["tool"] != "video" || body["input"].(map[string]any)["file_id"] != "file_1" || r.Header.Get("Idempotency-Key") == "" {
				t.Errorf("job body %v key %q", body, r.Header.Get("Idempotency-Key"))
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"job_1","tool":"video","status":"queued"}`))
		case r.URL.Path == "/v1/jobs/job_1":
			if polls.Add(1) < 3 {
				_, _ = w.Write([]byte(`{"id":"job_1","status":"running","progress":0.5}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"job_1","status":"succeeded","result":{"url":"https://x/y.webm","mime":"video/webm","bytes":10}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()
	a, err := c.Account(ctx)
	if err != nil || a.Credits != 70 || a.Balance.Paid != 50 || a.WebhookSecret != "s" {
		t.Fatalf("account %+v %v", a, err)
	}
	j, err := c.CreateJob(ctx, JobRequest{Tool: "video", Input: JobInput{FileID: "file_1"}, Params: map[string]any{"format": "webm"}})
	if err != nil || j.ID != "job_1" || j.Status != "queued" {
		t.Fatalf("job %+v %v", j, err)
	}
	done, err := c.WaitForJob(ctx, "job_1", 10*time.Millisecond)
	if err != nil || done.Status != "succeeded" || done.Result == nil || done.Result.URL != "https://x/y.webm" {
		t.Fatalf("done %+v %v", done, err)
	}
}

func TestVerifyWebhook(t *testing.T) {
	body := []byte(`{"type":"job.succeeded"}`)
	sign := func(ts int64, b []byte) string {
		m := hmac.New(sha256.New, []byte("secret"))
		fmt.Fprintf(m, "%d.%s", ts, b)
		return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(m.Sum(nil)))
	}
	now := time.Now().Unix()
	if !VerifyWebhook(body, sign(now, body), "secret", 0) {
		t.Error("valid signature rejected")
	}
	if VerifyWebhook(append(body, ' '), sign(now, body), "secret", 0) {
		t.Error("tampered body accepted")
	}
	if VerifyWebhook(body, sign(now, body), "other", 0) {
		t.Error("wrong secret accepted")
	}
	if VerifyWebhook(body, sign(now-3600, body), "secret", 0) {
		t.Error("stale timestamp accepted")
	}
	if VerifyWebhook(body, "garbage", "secret", 0) || VerifyWebhook(body, "", "secret", 0) {
		t.Error("malformed header accepted")
	}
}
