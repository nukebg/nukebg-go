package nukebg_test

// Live smoke test against a running NukeBG. Skipped unless NUKEBG_LIVE=1. Uses real credits on production.
//
//	NUKEBG_LIVE=1 NUKEBG_API_KEY=... NUKEBG_BASE_URL=http://localhost:3000 go test -run Live -v ./...

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	nukebg "github.com/nukebg/nukebg-go"
	"github.com/nukebg/nukebg-go/removebg"
)

const sample = "https://nukebg.com/samples/shoe.jpg"

func TestLive(t *testing.T) {
	if os.Getenv("NUKEBG_LIVE") != "1" {
		t.Skip("set NUKEBG_LIVE=1 to run against a real server")
	}
	ctx := context.Background()
	c := nukebg.NewClient("")

	before, err := c.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("credits before: %d", before.Credits)

	res, err := c.RemoveBackground(ctx, nukebg.ImageURL(sample), &nukebg.RemoveBackgroundParams{Background: "white", Size: "amazon", Format: "jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentType != "image/jpeg" || res.Width == 0 || res.CreditsCharged != 1 || len(res.Data) < 1000 {
		t.Errorf("url cutout: %s %dx%d charged %d", res.ContentType, res.Width, res.Height, res.CreditsCharged)
	}

	res2, err := c.RemoveBackground(ctx, nukebg.ImageBytes(mustGet(t, sample), "shoe.jpg"), &nukebg.RemoveBackgroundParams{Quality: "preview"})
	if err != nil {
		t.Fatal(err)
	}
	if res2.ContentType != "image/png" || res2.Foreground == nil {
		t.Errorf("bytes cutout: %s fg=%v", res2.ContentType, res2.Foreground)
	}

	_, err = c.RemoveBackground(ctx, nukebg.ImageURL(sample), &nukebg.RemoveBackgroundParams{Size: "nope"})
	var apiErr *nukebg.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_size" {
		t.Errorf("want invalid_size, got %v", err)
	}

	rb := removebg.NewClient("")
	rbRes, err := rb.RemoveBackground(ctx, removebg.Params{ImageURL: sample, Size: "preview"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rbRes.Data) < 1000 || rbRes.Width == 0 {
		t.Errorf("removebg compat: %d bytes %dx%d", len(rbRes.Data), rbRes.Width, rbRes.Height)
	}
}

func mustGet(t *testing.T, url string) []byte {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
