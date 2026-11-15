// Package nukebg is the official Go client for the NukeBG API (https://nukebg.com/docs):
// background removal with a true alpha channel, white and marketplace-ready backgrounds,
// object removal, upscaling, batches, video jobs and signed webhooks.
//
//	client := nukebg.NewClient("") // reads NUKEBG_API_KEY
//	res, err := client.RemoveBackground(ctx, nukebg.ImageFile("shoe.jpg"), &nukebg.RemoveBackgroundParams{
//		Background: "white",
//		Size:       "amazon",
//	})
//	if err != nil { ... }
//	err = res.Save("shoe.png")
//
// It uses only the standard library.
package nukebg

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Version is the SDK version, sent in the User-Agent header.
const Version = "1.0.0"

// DefaultBaseURL is the production API host.
const DefaultBaseURL = "https://api.nukebg.com"

// Client calls the NukeBG API. Create one with NewClient; it is safe for concurrent use.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	maxRetries int
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API host (default https://api.nukebg.com, or NUKEBG_BASE_URL).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient sets the http.Client used for requests (default: 120 s timeout).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// WithMaxRetries sets how many times 429 and 5xx responses are retried (default 2).
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// NewClient returns a client. An empty apiKey reads NUKEBG_API_KEY.
// Create a key at https://nukebg.com/account.
func NewClient(apiKey string, opts ...Option) *Client {
	if apiKey == "" {
		apiKey = os.Getenv("NUKEBG_API_KEY")
	}
	base := os.Getenv("NUKEBG_BASE_URL")
	if base == "" {
		base = DefaultBaseURL
	}
	c := &Client{apiKey: apiKey, baseURL: strings.TrimRight(base, "/"), httpClient: &http.Client{Timeout: 120 * time.Second}, maxRetries: 2}
	for _, o := range opts {
		o(c)
	}
	return c
}

// BaseURL returns the API host this client calls.
func (c *Client) BaseURL() string { return c.baseURL }

// ---------- inputs ----------

// Image is an input image: a public URL, a local file, raw bytes or a reader.
// Build one with ImageURL, ImageFile, ImageBytes or ImageReader.
type Image struct {
	url      string
	path     string
	data     []byte
	reader   io.Reader
	filename string
}

// ImageURL is a public http(s) image URL; NukeBG downloads it.
func ImageURL(u string) Image { return Image{url: u} }

// ImageFile reads a local JPG, PNG or WebP file.
func ImageFile(path string) Image { return Image{path: path, filename: filepath.Base(path)} }

// ImageBytes sends image bytes; filename is optional and only used for the upload's name.
func ImageBytes(data []byte, filename string) Image { return Image{data: data, filename: filename} }

// ImageReader reads the image from r when the request is built.
func ImageReader(r io.Reader, filename string) Image { return Image{reader: r, filename: filename} }

func (im Image) isURL() bool { return im.url != "" }

func (im Image) bytes() ([]byte, string, error) {
	name := im.filename
	if name == "" {
		name = "image"
	}
	switch {
	case im.data != nil:
		return im.data, name, nil
	case im.path != "":
		b, err := os.ReadFile(im.path)
		return b, name, err
	case im.reader != nil:
		b, err := io.ReadAll(im.reader)
		return b, name, err
	}
	return nil, "", errors.New("nukebg: empty Image; use ImageURL, ImageFile, ImageBytes or ImageReader")
}

// ---------- results ----------

// Box is the subject's bounding box in a result, in pixels.
type Box struct{ Left, Top, Width, Height int }

// ImageResult is a finished image and the details NukeBG reports about it.
type ImageResult struct {
	Data             []byte
	ContentType      string
	Width, Height    int
	Foreground       *Box // cutouts only
	CreditsCharged   int
	CreditsRemaining *int // nil when not reported
	RequestID        string
}

// Save writes the image to path.
func (r *ImageResult) Save(path string) error { return os.WriteFile(path, r.Data, 0o644) }

// Error is an API error. Use errors.As to inspect Code, for example "insufficient_credits".
type Error struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("nukebg: %s (%s, HTTP %d)", e.Message, e.Code, e.StatusCode)
}

// ---------- image tools ----------

// RemoveBackgroundParams are the options for RemoveBackground. Zero values use the API defaults.
type RemoveBackgroundParams struct {
	Quality        string   // preview | standard (default) | hd | ultra
	Background     string   // transparent (default) | white | #RRGGBB | gradient:#a,#b | scene:<id> | url:<https>
	Size           string   // original | amazon | shopify | etsy | ebay | instagram | story
	Width, Height  int      // output canvas, 1–8000 px
	Padding        *float64 // 0–45, % of the shorter side
	Crop           *bool
	Shadow         string // none | soft | contact
	Subject        string // all | main
	Despill        *bool
	Format         string // png | webp | jpg
	JPEGQuality    int
	Channels       string // rgba | alpha
	IdempotencyKey string // generated when empty
}

// RemoveBackground cuts out the subject (1 credit; HD 2, Ultra 4) and optionally places it on a background.
func (c *Client) RemoveBackground(ctx context.Context, img Image, p *RemoveBackgroundParams) (*ImageResult, error) {
	if p == nil {
		p = &RemoveBackgroundParams{}
	}
	f := fields{}
	f.str("quality", p.Quality)
	f.str("background", p.Background)
	f.str("size", p.Size)
	f.num("width", p.Width)
	f.num("height", p.Height)
	f.float("padding", p.Padding)
	f.boolean("crop", p.Crop)
	f.str("shadow", p.Shadow)
	f.str("subject", p.Subject)
	f.boolean("despill", p.Despill)
	f.str("format", p.Format)
	f.num("jpeg_quality", p.JPEGQuality)
	f.str("channels", p.Channels)
	return c.imageCall(ctx, "/v1/cutouts", img, f, nil, p.IdempotencyKey)
}

// EraseParams are the options for Erase. Set Prompt, Mask or both.
type EraseParams struct {
	Prompt         string // what to remove, e.g. "the cables on the desk"
	Mask           *Image // white = remove
	Quality        string // fast (default) | enhance
	Format         string
	IdempotencyKey string
}

// Erase removes objects (fast 25 credits, enhance 40). Pixels outside the edit are returned unchanged.
func (c *Client) Erase(ctx context.Context, img Image, p EraseParams) (*ImageResult, error) {
	if p.Prompt == "" && p.Mask == nil {
		return nil, errors.New("nukebg: Erase needs a Prompt, a Mask, or both")
	}
	f := fields{}
	f.str("prompt", p.Prompt)
	f.str("quality", p.Quality)
	f.str("format", p.Format)
	var extra map[string]Image
	if p.Mask != nil {
		extra = map[string]Image{"mask_file": *p.Mask}
	}
	return c.imageCall(ctx, "/v1/erase", img, f, extra, p.IdempotencyKey)
}

// UpscaleParams are the options for Upscale.
type UpscaleParams struct {
	Scale          int // 2 (default) or 4
	Format         string
	IdempotencyKey string
}

// Upscale enlarges an image 2× (source up to 16 MP) or 4× (up to 4 MP). Needs paid credits.
func (c *Client) Upscale(ctx context.Context, img Image, p UpscaleParams) (*ImageResult, error) {
	f := fields{}
	f.num("scale", p.Scale)
	f.str("format", p.Format)
	return c.imageCall(ctx, "/v1/upscale", img, f, nil, p.IdempotencyKey)
}

// Account is the credit balance for the key.
type Account struct {
	Account string `json:"account"`
	Credits int    `json:"credits"`
	Balance struct {
		Free         int `json:"free"`
		Subscription int `json:"subscription"`
		Paid         int `json:"paid"`
	} `json:"balance"`
	Plan          string `json:"plan"`
	WebhookSecret string `json:"webhook_secret"`
}

// Account returns the credit balance, plan and webhook secret.
func (c *Client) Account(ctx context.Context) (*Account, error) {
	var a Account
	return &a, c.jsonCall(ctx, http.MethodGet, "/v1/account", nil, "", &a)
}

// ---------- uploads, jobs and batches ----------

// Upload sends a large file straight to storage and returns its file_id for CreateJob or CreateBatch.
// An empty contentType is guessed from the file name.
func (c *Client) Upload(ctx context.Context, file Image, contentType string) (string, error) {
	data, name, err := file.bytes()
	if err != nil {
		return "", err
	}
	if contentType == "" {
		contentType = guessType(name)
	}
	var slot struct {
		FileID    string            `json:"file_id"`
		UploadURL string            `json:"upload_url"`
		Headers   map[string]string `json:"headers"`
	}
	if err := c.jsonCall(ctx, http.MethodPost, "/v1/uploads", map[string]any{"content_type": contentType, "size": len(data)}, "", &slot); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, slot.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range slot.Headers {
		req.Header.Set(k, v)
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode >= 300 {
		return "", &Error{StatusCode: res.StatusCode, Code: "upload_failed", Message: "upload to storage failed"}
	}
	return slot.FileID, nil
}

// JobInput points a job at an uploaded file or a public URL.
type JobInput struct {
	FileID   string `json:"file_id,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// Job is a background job.
type Job struct {
	ID             string     `json:"id"`
	Tool           string     `json:"tool"`
	Status         string     `json:"status"` // queued | running | succeeded | failed | cancelled
	Progress       float64    `json:"progress"`
	BatchID        *string    `json:"batch_id"`
	CreditsHeld    int        `json:"credits_held"`
	CreditsCharged int        `json:"credits_charged"`
	CreatedAt      string     `json:"created_at"`
	StartedAt      *string    `json:"started_at"`
	FinishedAt     *string    `json:"finished_at"`
	Error          *JobError  `json:"error"`
	Result         *JobResult `json:"result"`
}

// JobError explains a failed job.
type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JobResult is where a finished job's output can be downloaded (valid for 24 hours).
type JobResult struct {
	URL     string   `json:"url"`
	Mime    string   `json:"mime"`
	Bytes   int64    `json:"bytes"`
	Width   *int     `json:"width"`
	Height  *int     `json:"height"`
	Seconds *float64 `json:"seconds"`
}

// Done reports whether the job has finished, successfully or not.
func (j *Job) Done() bool {
	return j.Status == "succeeded" || j.Status == "failed" || j.Status == "cancelled"
}

// JobRequest creates a job. Tool is cutout, erase, upscale, video or video-clean.
type JobRequest struct {
	Tool           string         `json:"tool"`
	Input          JobInput       `json:"input"`
	Params         map[string]any `json:"params,omitempty"`
	WebhookURL     string         `json:"webhook_url,omitempty"`
	IdempotencyKey string         `json:"-"`
}

// CreateJob starts a background job. Credits are held now and charged on success.
func (c *Client) CreateJob(ctx context.Context, r JobRequest) (*Job, error) {
	var j Job
	return &j, c.jsonCall(ctx, http.MethodPost, "/v1/jobs", r, r.IdempotencyKey, &j)
}

// GetJob returns a job's status and, once succeeded, its result.
func (c *Client) GetJob(ctx context.Context, id string) (*Job, error) {
	var j Job
	return &j, c.jsonCall(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(id), nil, "", &j)
}

// CancelJob cancels a queued job.
func (c *Client) CancelJob(ctx context.Context, id string) (*Job, error) {
	var j Job
	return &j, c.jsonCall(ctx, http.MethodDelete, "/v1/jobs/"+url.PathEscape(id), nil, "", &j)
}

// WaitForJob polls every interval (default 2 s) until the job finishes or ctx is done.
func (c *Client) WaitForJob(ctx context.Context, id string, interval time.Duration) (*Job, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		j, err := c.GetJob(ctx, id)
		if err != nil || j.Done() {
			return j, err
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// BatchRequest runs the same tool (cutout or upscale) on up to 500 items.
type BatchRequest struct {
	Tool       string         `json:"tool"`
	Items      []JobInput     `json:"items"`
	Params     map[string]any `json:"params,omitempty"`
	WebhookURL string         `json:"webhook_url,omitempty"`
}

// BatchCreated is the response to CreateBatch.
type BatchCreated struct {
	BatchID  string `json:"batch_id"`
	Queued   int    `json:"queued"`
	Rejected []struct {
		Index int      `json:"index"`
		Error JobError `json:"error"`
	} `json:"rejected"`
	Jobs []Job `json:"jobs"`
}

// Batch is a batch's progress.
type Batch struct {
	BatchID        string  `json:"batch_id"`
	Status         string  `json:"status"` // processing | completed
	Total          int     `json:"total"`
	Succeeded      int     `json:"succeeded"`
	Failed         int     `json:"failed"`
	Running        int     `json:"running"`
	Queued         int     `json:"queued"`
	CreditsCharged int     `json:"credits_charged"`
	ZipURL         *string `json:"zip_url"`
	Jobs           []Job   `json:"jobs"`
}

// CreateBatch queues up to 500 items.
func (c *Client) CreateBatch(ctx context.Context, r BatchRequest) (*BatchCreated, error) {
	var b BatchCreated
	return &b, c.jsonCall(ctx, http.MethodPost, "/v1/batches", r, "", &b)
}

// GetBatch returns a batch's progress.
func (c *Client) GetBatch(ctx context.Context, id string) (*Batch, error) {
	var b Batch
	return &b, c.jsonCall(ctx, http.MethodGet, "/v1/batches/"+url.PathEscape(id), nil, "", &b)
}

// BatchZip downloads every successful result of a finished batch as one ZIP.
func (c *Client) BatchZip(ctx context.Context, id string) ([]byte, error) {
	res, err := c.do(ctx, http.MethodGet, "/v1/batches/"+url.PathEscape(id)+"/zip", nil, "", "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(res.Body)
}

// ---------- webhooks ----------

// VerifyWebhook checks an X-NukeBG-Signature header ("t=<unix>,v1=<hex>"): an HMAC-SHA256 of
// "t.body" with your webhook secret (from Account). Pass the raw request body. A tolerance of 0 means 5 minutes.
func VerifyWebhook(rawBody []byte, signatureHeader, secret string, tolerance time.Duration) bool {
	if tolerance <= 0 {
		tolerance = 5 * time.Minute
	}
	var t int64
	var v1 string
	for _, part := range strings.Split(signatureHeader, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			t, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			v1 = v
		}
	}
	if t == 0 || v1 == "" || math.Abs(float64(time.Now().Unix()-t)) > tolerance.Seconds() {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", t)
	mac.Write(rawBody)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(v1))
}

// ---------- internals ----------

type fields [][2]string

func (f *fields) str(k, v string) {
	if v != "" {
		*f = append(*f, [2]string{k, v})
	}
}
func (f *fields) num(k string, v int) {
	if v != 0 {
		*f = append(*f, [2]string{k, strconv.Itoa(v)})
	}
}
func (f *fields) float(k string, v *float64) {
	if v != nil {
		*f = append(*f, [2]string{k, strconv.FormatFloat(*v, 'f', -1, 64)})
	}
}
func (f *fields) boolean(k string, v *bool) {
	if v != nil {
		*f = append(*f, [2]string{k, strconv.FormatBool(*v)})
	}
}

func (c *Client) imageCall(ctx context.Context, path string, img Image, f fields, files map[string]Image, key string) (*ImageResult, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, kv := range f {
		if err := w.WriteField(kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	if img.isURL() {
		if err := w.WriteField("image_url", img.url); err != nil {
			return nil, err
		}
	} else if err := writeFile(w, "image_file", img); err != nil {
		return nil, err
	}
	for name, im := range files {
		if err := writeFile(w, name, im); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if key == "" {
		key = newKey()
	}
	res, err := c.do(ctx, http.MethodPost, path, buf.Bytes(), w.FormDataContentType(), key)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	h := res.Header
	out := &ImageResult{Data: data, ContentType: h.Get("Content-Type"), RequestID: h.Get("X-Request-Id")}
	out.Width, _ = strconv.Atoi(h.Get("X-Width"))
	out.Height, _ = strconv.Atoi(h.Get("X-Height"))
	out.CreditsCharged, _ = strconv.Atoi(h.Get("X-Credits-Charged"))
	if n, err := strconv.Atoi(h.Get("X-Credits-Remaining")); err == nil {
		out.CreditsRemaining = &n
	}
	if parts := strings.Split(h.Get("X-Foreground"), ","); len(parts) == 4 {
		var b [4]int
		ok := true
		for i, s := range parts {
			v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			ok = ok && err == nil
			b[i] = int(v)
		}
		if ok {
			out.Foreground = &Box{Left: b[0], Top: b[1], Width: b[2], Height: b[3]}
		}
	}
	return out, nil
}

func writeFile(w *multipart.Writer, field string, im Image) error {
	data, name, err := im.bytes()
	if err != nil {
		return err
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, name))
	h.Set("Content-Type", guessType(name))
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func (c *Client) jsonCall(ctx context.Context, method, path string, body any, key string, out any) error {
	var payload []byte
	ctype := ""
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
		ctype = "application/json"
		if key == "" && method == http.MethodPost {
			key = newKey()
		}
	}
	res, err := c.do(ctx, method, path, payload, ctype, key)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return json.NewDecoder(res.Body).Decode(out)
}

// do sends the request, retrying 429 and 5xx (honouring Retry-After) with the same body and idempotency key.
func (c *Client) do(ctx context.Context, method, path string, body []byte, contentType, key string) (*http.Response, error) {
	if c.apiKey == "" {
		return nil, errors.New("nukebg: no API key; pass one to NewClient or set NUKEBG_API_KEY (create one at https://nukebg.com/account)")
	}
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Api-Key", c.apiKey)
		req.Header.Set("User-Agent", "nukebg-go/"+Version)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if res.StatusCode < 400 {
			return res, nil
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if (res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500) && attempt < c.maxRetries {
			wait := time.Duration(500*(1<<attempt)) * time.Millisecond
			if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		return nil, decodeError(res.StatusCode, res.Header.Get("X-Request-Id"), raw)
	}
}

func decodeError(status int, requestID string, raw []byte) error {
	var body struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	e := &Error{StatusCode: status, Code: "http_error", Message: http.StatusText(status), RequestID: requestID}
	if json.Unmarshal(raw, &body) == nil && body.Error.Code != "" {
		e.Code, e.Message = body.Error.Code, body.Error.Message
		if body.Error.RequestID != "" {
			e.RequestID = body.Error.RequestID
		}
	}
	return e
}

func newKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func guessType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	}
	return "application/octet-stream"
}
