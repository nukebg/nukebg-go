// Package removebg is a remove.bg-compatible client backed by NukeBG.
//
// It takes remove.bg's request parameters and returns remove.bg's result details, calling
// POST https://api.nukebg.com/v1.0/removebg, so code written against the remove.bg API keeps its
// parameter names. remove.bg's self-serve API closes on 1 December 2026.
//
//	c := removebg.NewClient("")                       // reads NUKEBG_API_KEY
//	res, err := c.RemoveBackground(ctx, removebg.Params{ImageFilePath: "photo.jpg", Size: "auto"})
//	if err != nil { ... }
//	os.WriteFile("photo.png", res.Data, 0o644)
package removebg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	nukebg "github.com/nukebg/nukebg-go"
)

// Params are remove.bg's request parameters. Set one of ImageURL, ImageFilePath or ImageFileB64.
type Params struct {
	ImageURL      string `json:"image_url,omitempty"`
	ImageFilePath string `json:"-"`
	ImageFileB64  string `json:"image_file_b64,omitempty"`

	Size             string `json:"size,omitempty"`   // preview | small | regular | medium | hd | full | 4k | auto | 50mp
	Type             string `json:"type,omitempty"`   // accepted, ignored
	Format           string `json:"format,omitempty"` // auto | png | jpg | webp | zip
	ROI              string `json:"roi,omitempty"`
	Crop             bool   `json:"crop,omitempty"`
	CropMargin       string `json:"crop_margin,omitempty"`
	Scale            string `json:"scale,omitempty"`
	Position         string `json:"position,omitempty"`
	Channels         string `json:"channels,omitempty"` // rgba | alpha
	BgColor          string `json:"bg_color,omitempty"`
	BgImageURL       string `json:"bg_image_url,omitempty"`
	Semitransparency *bool  `json:"semitransparency,omitempty"` // accepted, ignored
}

// Result is a finished image with remove.bg's response details.
type Result struct {
	Data             []byte
	Width, Height    int
	CreditsCharged   int
	DetectedType     string
	ForegroundTop    int
	ForegroundLeft   int
	ForegroundWidth  int
	ForegroundHeight int
}

// APIError carries remove.bg-style errors: [{"title", "code"}].
type APIError struct {
	StatusCode int
	Errors     []struct {
		Title string `json:"title"`
		Code  string `json:"code"`
	}
}

func (e *APIError) Error() string {
	if len(e.Errors) > 0 {
		return fmt.Sprintf("removebg: %s (%s, HTTP %d)", e.Errors[0].Title, e.Errors[0].Code, e.StatusCode)
	}
	return fmt.Sprintf("removebg: HTTP %d", e.StatusCode)
}

// Client calls NukeBG's remove.bg-compatible endpoint.
type Client struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// NewClient returns a client; an empty apiKey reads NUKEBG_API_KEY.
func NewClient(apiKey string) *Client {
	if apiKey == "" {
		apiKey = os.Getenv("NUKEBG_API_KEY")
	}
	base := os.Getenv("NUKEBG_BASE_URL")
	if base == "" {
		base = nukebg.DefaultBaseURL
	}
	return &Client{APIKey: apiKey, BaseURL: base, HTTPClient: &http.Client{Timeout: 120 * time.Second}}
}

// RemoveBackground removes the background with remove.bg's parameters.
func (c *Client) RemoveBackground(ctx context.Context, p Params) (*Result, error) {
	if p.ImageFilePath != "" {
		b, err := os.ReadFile(p.ImageFilePath)
		if err != nil {
			return nil, err
		}
		p.ImageFileB64 = base64.StdEncoding.EncodeToString(b)
	}
	if p.ImageURL == "" && p.ImageFileB64 == "" {
		return nil, errors.New("removebg: set ImageURL, ImageFilePath or ImageFileB64")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/v1.0/removebg", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "nukebg-go/"+nukebg.Version+" (removebg-compat)")
	res, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		e := &APIError{StatusCode: res.StatusCode}
		_ = json.Unmarshal(raw, e)
		return nil, e
	}
	var payload struct {
		Data struct {
			ResultB64        string  `json:"result_b64"`
			ForegroundTop    float64 `json:"foreground_top"`
			ForegroundLeft   float64 `json:"foreground_left"`
			ForegroundWidth  float64 `json:"foreground_width"`
			ForegroundHeight float64 `json:"foreground_height"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(payload.Data.ResultB64)
	if err != nil {
		return nil, err
	}
	r := &Result{
		Data:             data,
		DetectedType:     res.Header.Get("X-Type"),
		ForegroundTop:    int(payload.Data.ForegroundTop),
		ForegroundLeft:   int(payload.Data.ForegroundLeft),
		ForegroundWidth:  int(payload.Data.ForegroundWidth),
		ForegroundHeight: int(payload.Data.ForegroundHeight),
	}
	r.Width, _ = strconv.Atoi(res.Header.Get("X-Width"))
	r.Height, _ = strconv.Atoi(res.Header.Get("X-Height"))
	r.CreditsCharged, _ = strconv.Atoi(res.Header.Get("X-Credits-Charged"))
	return r, nil
}
