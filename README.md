# nukebg-go

Official Go client for the [NukeBG](https://nukebg.com) background removal API: transparent cutouts, white and marketplace-ready backgrounds, object removal, upscaling, batches, video jobs and webhooks. Standard library only, Go 1.21+.

Includes a **remove.bg-compatible client** (`removebg` package). remove.bg's self-serve API closes on 1 December 2026.

```bash
go get github.com/nukebg/nukebg-go
```

Create an API key at [nukebg.com/account](https://nukebg.com/account). New accounts get 50 free credits plus 20 a month, and one standard cutout is one credit.

## Remove a background

```go
client := nukebg.NewClient("") // reads NUKEBG_API_KEY

res, err := client.RemoveBackground(ctx, nukebg.ImageFile("shoe.jpg"), &nukebg.RemoveBackgroundParams{
	Background: "white",  // or "#F4F4F4", "gradient:#fff,#eee", "scene:marble"; empty = transparent
	Size:       "amazon", // original | amazon | shopify | etsy | ebay | instagram | story
	Format:     "jpg",
})
if err != nil {
	log.Fatal(err)
}
res.Save("shoe-amazon.jpg")
fmt.Println(res.Width, res.Height, *res.CreditsRemaining)
```

Inputs: `nukebg.ImageFile(path)`, `nukebg.ImageURL(url)`, `nukebg.ImageBytes(data, name)` or `nukebg.ImageReader(r, name)`.

## Other tools

```go
clean, err := client.Erase(ctx, nukebg.ImageFile("desk.jpg"), nukebg.EraseParams{Prompt: "the cables on the desk"})
big, err := client.Upscale(ctx, nukebg.ImageFile("small.jpg"), nukebg.UpscaleParams{Scale: 4}) // paid credits
acct, err := client.Account(ctx)
```

## Batches, video and webhooks

```go
batch, err := client.CreateBatch(ctx, nukebg.BatchRequest{
	Tool:   "cutout",
	Items:  []nukebg.JobInput{{ImageURL: "https://example.com/1.jpg"}},
	Params: map[string]any{"background": "white"},
})
status, err := client.GetBatch(ctx, batch.BatchID)
zip, err := client.BatchZip(ctx, batch.BatchID)

fileID, err := client.Upload(ctx, nukebg.ImageFile("clip.mp4"), "")
job, err := client.CreateJob(ctx, nukebg.JobRequest{Tool: "video", Input: nukebg.JobInput{FileID: fileID}, Params: map[string]any{"format": "webm"}})
done, err := client.WaitForJob(ctx, job.ID, 0)
fmt.Println(done.Result.URL)

// In your webhook handler (raw body, before any JSON decoding)
ok := nukebg.VerifyWebhook(body, r.Header.Get("X-NukeBG-Signature"), acct.WebhookSecret, 0)
```

## Switching from remove.bg

```go
import "github.com/nukebg/nukebg-go/removebg"

c := removebg.NewClient(os.Getenv("NUKEBG_API_KEY"))
res, err := c.RemoveBackground(ctx, removebg.Params{ImageFilePath: "photo.jpg", Size: "auto", BgColor: "white"})
os.WriteFile("photo.png", res.Data, 0o644)
```

`removebg.Params` uses remove.bg's parameter names (`size`, `format`, `roi`, `crop`, `crop_margin`, `scale`, `position`, `channels`, `bg_color`, `bg_image_url`), and errors keep remove.bg's `[{title, code}]` shape (`*removebg.APIError`). If you call the remove.bg REST API directly, you can also just change the host to `https://api.nukebg.com` and the key. See the [migration guide](https://nukebg.com/migrate-from-remove-bg).

## Errors and retries

API errors are `*nukebg.Error` with `StatusCode`, `Code` (for example `insufficient_credits`, `invalid_api_key`, `no_subject`) and `RequestID`; use `errors.As`. Rate limits (429) and temporary engine errors (5xx) are retried twice with the same idempotency key, honouring `Retry-After`, so a retry is never charged twice. Failed jobs are never charged.

## Tests

```bash
go test ./...                                                       # unit tests, no network
NUKEBG_LIVE=1 NUKEBG_API_KEY=... go test -run Live -v ./...          # live, uses credits
```

MIT licensed. NukeBG is a product of VOLODS LTD. Not affiliated with remove.bg.
