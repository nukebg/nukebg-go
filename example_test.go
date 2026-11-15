package nukebg_test

import (
	"context"
	"errors"
	"fmt"
	"log"

	nukebg "github.com/nukebg/nukebg-go"
)

func Example() {
	client := nukebg.NewClient("") // reads NUKEBG_API_KEY
	ctx := context.Background()

	res, err := client.RemoveBackground(ctx, nukebg.ImageFile("shoe.jpg"), &nukebg.RemoveBackgroundParams{
		Background: "white",
		Size:       "amazon",
		Format:     "jpg",
	})
	var apiErr *nukebg.Error
	if errors.As(err, &apiErr) && apiErr.Code == "insufficient_credits" {
		log.Fatal("top up at https://nukebg.com/pricing")
	} else if err != nil {
		log.Fatal(err)
	}
	if err := res.Save("shoe-amazon.jpg"); err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Width, res.Height, res.CreditsCharged)
}

func ExampleClient_CreateBatch() {
	client := nukebg.NewClient("")
	ctx := context.Background()
	batch, err := client.CreateBatch(ctx, nukebg.BatchRequest{
		Tool:   "cutout",
		Items:  []nukebg.JobInput{{ImageURL: "https://example.com/1.jpg"}, {ImageURL: "https://example.com/2.jpg"}},
		Params: map[string]any{"background": "white", "size": "shopify"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(batch.BatchID, batch.Queued)
}
