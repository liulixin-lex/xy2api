package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	apperrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

type CodexProxyTestInput struct {
	ID               string `json:"id"`
	ExpectedRevision string `json:"expected_revision"`
	URL              string `json:"url"`
}

func (s *OpenAIGatewayService) GetCodexHarvestProxies(ctx context.Context) (*CodexHarvestProxyPool, error) {
	if s.settingService == nil {
		return nil, apperrors.New(503, "CODEX_PROXY_UNAVAILABLE", "Proxy settings unavailable")
	}
	return s.settingService.GetCodexHarvestProxyPool(ctx)
}
func (s *OpenAIGatewayService) SaveCodexHarvestProxies(ctx context.Context, input CodexHarvestProxyPoolUpdate) (*CodexHarvestProxyPool, error) {
	if s.settingService == nil {
		return nil, apperrors.New(503, "CODEX_PROXY_UNAVAILABLE", "Proxy settings unavailable")
	}
	return s.settingService.SaveCodexHarvestProxyPool(ctx, input)
}
func (s *OpenAIGatewayService) TestCodexHarvestProxy(ctx context.Context, input CodexProxyTestInput) (*CodexProxyProbeResult, error) {
	if s.settingService == nil {
		return nil, apperrors.New(503, "CODEX_PROXY_UNAVAILABLE", "Proxy settings unavailable")
	}
	if (input.ID == "") == (strings.TrimSpace(input.URL) == "") {
		return nil, apperrors.BadRequest("CODEX_PROXY_TEST", "Provide either a saved proxy ID or a draft URL")
	}
	entry := CodexHarvestProxy{URL: strings.TrimSpace(input.URL)}
	if input.ID != "" {
		pool, err := s.GetCodexHarvestProxies(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, e := range pool.Entries {
			if e.ID == input.ID {
				entry = e
				found = true
				break
			}
		}
		if !found {
			return nil, apperrors.New(404, "CODEX_PROXY_NOT_FOUND", "Proxy not found")
		}
		if input.ExpectedRevision == "" || entry.Revision != input.ExpectedRevision {
			return nil, ErrCodexTicketConflict
		}
	}
	if ValidateOpenAICodexTicketHarvestProxyURL(entry.URL) != nil {
		return nil, apperrors.BadRequest("CODEX_PROXY_URL", "Invalid acquisition proxy URL")
	}
	digest := sha256.Sum256([]byte(entry.ID + ":" + entry.Revision + ":" + entry.URL))
	key := hex.EncodeToString(digest[:])
	result := s.settingService.codexProxyProbeSF.DoChan(key, func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.settingService.codexProxyProbeOnce.Do(func() { s.settingService.codexProxyProbeSlots = make(chan struct{}, 3) })
		select {
		case s.settingService.codexProxyProbeSlots <- struct{}{}:
			defer func() { <-s.settingService.codexProxyProbeSlots }()
		case <-probeCtx.Done():
			return nil, apperrors.New(503, "CODEX_PROXY_TEST_BUSY", "Proxy tests are busy; try again")
		}
		r := s.probeCodexProxyExit(probeCtx, entry.URL)
		if entry.ID != "" {
			saveCtx, saveCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer saveCancel()
			err := s.settingService.updateCodexProxyHealth(saveCtx, entry, func(h *codexProxyHealth) {
				h.Probe = r
				if r.Success {
					h.LastSuccess = r
					h.NetworkFailures = 0
					h.RetryAt = time.Time{}
				}
			})
			if err != nil {
				return nil, err
			}
		}
		return r, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		probe, ok := r.Val.(*CodexProxyProbeResult)
		if !ok || probe == nil {
			return nil, apperrors.New(503, "CODEX_PROXY_TEST_RESULT", "Proxy test result unavailable")
		}
		return probe, nil
	}
}
func (s *OpenAIGatewayService) probeCodexProxyExit(ctx context.Context, rawURL string) *CodexProxyProbeResult {
	started := time.Now()
	result := &CodexProxyProbeResult{CheckedAt: started, Message: "connection_failed"}
	defer func() { result.LatencyMS = time.Since(started).Milliseconds() }()
	targets := []string{"https://www.cloudflare.com/cdn-cgi/trace"}
	if s.cfg != nil && len(s.cfg.Security.ProxyProbe.URLs) > 0 {
		targets = nil
		for _, target := range s.cfg.Security.ProxyProbe.URLs {
			targets = append(targets, target.URL)
		}
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			continue
		}
		req.Close = true
		req = req.WithContext(WithHTTPUpstreamSingleAttempt(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileOpenAIHarvest))))
		proxyURL := freshCodexTicketProxyURL(rawURL)
		var resp *http.Response
		if s.openAICodexTicketConfig().HarvestDialProxyURL != "" {
			client, e := newCodexTicketChainedClient(proxyURL, s.openAICodexTicketConfig().HarvestDialProxyURL)
			if e != nil {
				continue
			}
			resp, err = client.Do(req)
			client.CloseIdleConnections()
		} else if s.httpUpstream != nil {
			resp, err = s.httpUpstream.Do(req, proxyURL, 0, 1)
		} else {
			err = errors.New("transport unavailable")
		}
		if err != nil || resp == nil || resp.Body == nil {
			continue
		}
		body, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		_ = resp.Body.Close()
		if e != nil || len(body) > 1<<20 || resp.StatusCode != http.StatusOK {
			continue
		}
		ip, country, code := ParseCodexProxyExit(body)
		if ip == "" {
			continue
		}
		result.Success = true
		result.IPAddress = ip
		result.Country = country
		result.CountryCode = code
		result.Message = "connected"
		break
	}
	return result
}

// IP and country always come from one response, never separate rotating exits.
func ParseCodexProxyExit(body []byte) (ip, country, code string) {
	var data struct {
		IP      string `json:"ip"`
		Query   string `json:"query"`
		Country string `json:"country"`
		Code    string `json:"countryCode"`
	}
	if json.Unmarshal(body, &data) == nil {
		ip = data.IP
		if ip == "" {
			ip = data.Query
		}
		country = data.Country
		code = data.Code
	} else {
		for _, line := range strings.Split(string(body), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			switch key {
			case "ip":
				ip = strings.TrimSpace(value)
			case "loc":
				code = strings.TrimSpace(value)
			}
		}
	}
	if net.ParseIP(ip) == nil {
		return "", "", ""
	}
	code = strings.ToUpper(code)
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		code = ""
	}
	if len(country) > 100 {
		country = ""
	}
	return
}
func (s *OpenAIGatewayService) codexProxySnapshot() *CodexHarvestProxyPool {
	if s.settingService == nil {
		return nil
	}
	pool, _ := s.settingService.codexProxySnapshot.Load().(*CodexHarvestProxyPool)
	return pool
}
func (s *OpenAIGatewayService) selectCodexHarvestProxy(rt *codexTicketRuntime, now time.Time) (CodexHarvestProxy, string, bool) {
	pool := s.codexProxySnapshot()
	if pool == nil {
		return CodexHarvestProxy{}, "", true
	}
	if len(pool.Entries) == 0 {
		return CodexHarvestProxy{}, pool.Revision, false
	}
	for offset := 0; offset < len(pool.Entries); offset++ {
		i := (rt.ProxyCursor + offset) % len(pool.Entries)
		entry := pool.Entries[i]
		if !entry.Enabled || entry.RetryAt.After(now) || rt.RequestedProxyID != "" && entry.ID != rt.RequestedProxyID {
			continue
		}
		rt.ProxyCursor = (i + 1) % len(pool.Entries)
		return entry, pool.Revision, true
	}
	return CodexHarvestProxy{}, pool.Revision, false
}
func (s *OpenAIGatewayService) recordCodexProxyAcquisition(ctx context.Context, job *codexAccountTicketJob, status int, err error) {
	if job.proxy.ID == "" || s.settingService == nil {
		return
	}
	var networkError net.Error
	_ = s.settingService.updateCodexProxyHealth(ctx, job.proxy, func(h *codexProxyHealth) {
		switch {
		case status == 0 && errors.As(err, &networkError):
			h.NetworkFailures++
			delay := 30 * time.Second * time.Duration(1<<min(h.NetworkFailures-1, 4))
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
			h.RetryAt = time.Now().Add(delay)
			h.LastAcquisition = "network_error"
		case err != nil:
			h.LastAcquisition = "candidate_rejected"
		default:
			h.NetworkFailures = 0
			h.RetryAt = time.Time{}
			h.LastAcquisition = "response_received"
		}
	})
}
