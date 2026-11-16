package apicompat

import (
	"encoding/json"
	"net/url"
	"strings"
)

// responsesWebSearchSources projects only sources actually supplied upstream.
func responsesWebSearchSources(item ResponsesOutput) json.RawMessage {
	type result struct {
		Type    string `json:"type"`
		URL     string `json:"url"`
		Title   string `json:"title,omitempty"`
		Content string `json:"content,omitempty"`
	}
	results := []result{}
	if item.Status == "completed" && item.Action != nil {
		sources := item.Action.Sources
		if item.Action.Type == "open_page" && item.Action.URL != "" {
			sources = append(append([]ResponsesWebSearchSource(nil), sources...), ResponsesWebSearchSource{URL: item.Action.URL})
		}
		seen := make(map[string]bool)
		for _, source := range sources {
			u, err := url.Parse(source.URL)
			if err != nil || u.Host == "" || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) || seen[source.URL] {
				continue
			}
			seen[source.URL] = true
			results = append(results, result{Type: "web_search_result", URL: source.URL, Title: source.Title, Content: source.Content})
		}
	}
	raw, _ := json.Marshal(results)
	return raw
}
