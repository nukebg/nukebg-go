package removebg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveBackgroundFromFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.0/removebg" || r.Header.Get("X-Api-Key") != "k" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("%s %v", r.URL.Path, r.Header)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["image_file_b64"] != base64.StdEncoding.EncodeToString([]byte("JPEG")) || body["size"] != "auto" || body["bg_color"] != "white" || body["crop"] != true {
			t.Errorf("body %v", body)
		}
		if _, ok := body["type"]; ok {
			t.Error("empty fields must be omitted")
		}
		w.Header().Set("X-Width", "100")
		w.Header().Set("X-Height", "80")
		w.Header().Set("X-Credits-Charged", "1")
		w.Header().Set("X-Type", "product")
		_, _ = w.Write([]byte(`{"data":{"result_b64":"` + base64.StdEncoding.EncodeToString([]byte("PNG")) + `","foreground_top":1,"foreground_left":2,"foreground_width":30,"foreground_height":40}}`))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "photo.jpg")
	_ = os.WriteFile(path, []byte("JPEG"), 0o644)
	c := NewClient("k")
	c.BaseURL = srv.URL
	res, err := c.RemoveBackground(context.Background(), Params{ImageFilePath: path, Size: "auto", BgColor: "white", Crop: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != "PNG" || res.Width != 100 || res.Height != 80 || res.CreditsCharged != 1 || res.DetectedType != "product" || res.ForegroundWidth != 30 || res.ForegroundLeft != 2 {
		t.Errorf("result %+v", res)
	}
}

func TestRemoveBgErrorFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"title":"API Key invalid","code":"auth_failed"}]}`))
	}))
	defer srv.Close()
	c := NewClient("bad")
	c.BaseURL = srv.URL
	_, err := c.RemoveBackground(context.Background(), Params{ImageURL: "https://example.com/a.jpg"})
	var e *APIError
	if !errors.As(err, &e) || e.StatusCode != 403 || len(e.Errors) != 1 || e.Errors[0].Code != "auth_failed" {
		t.Fatalf("err %T %v", err, err)
	}
	if _, err := c.RemoveBackground(context.Background(), Params{}); err == nil {
		t.Error("want an error without an image")
	}
}
