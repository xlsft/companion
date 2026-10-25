package anthropic

import (
	"context"
	"net/http"
	"strconv"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/llm/internal/wire"
)

func init() { llm.RegisterLister(llm.KindAnthropic, ListModels) }

// pageSize is the API's largest page, so the official list is one call.
const pageSize = 1000

type modelsPage struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// ListModels reads GET {BaseURL}/v1/models page by page, up to
// llm.MaxModels ids. Prefer llm.ListModels, which validates the config
// and hardens the client.
func ListModels(ctx context.Context, cfg llm.ProviderConfig, apiKey string, client *http.Client) ([]string, error) {
	u, err := apiURL(cfg, "models")
	if err != nil {
		return nil, err
	}
	h := headers(cfg, apiKey)
	var ids []string
	after := ""
	for len(ids) < llm.MaxModels {
		q := u.Query()
		q.Set("limit", strconv.Itoa(pageSize))
		if after != "" {
			q.Set("after_id", after)
		}
		u.RawQuery = q.Encode()
		var page modelsPage
		if err := wire.GetJSON(ctx, wire.Get{Client: client, URL: u.String(), Header: h,
			Timeout: cfg.Timeout, ParseError: parseError}, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Data {
			ids = append(ids, m.ID)
		}
		// A server that repeats a cursor would loop forever.
		if !page.HasMore || len(page.Data) == 0 || page.LastID == "" || page.LastID == after {
			break
		}
		after = page.LastID
	}
	return ids, nil
}
