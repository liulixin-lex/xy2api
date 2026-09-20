package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const codexTicketQualityKey = "codex_ticket_quality"
const codexQualityVersion = "state-quality-20260920-v1"

type codexQualityQuestion struct {
	Category string
	Prompt   string
	Answer   string
}

var codexQualityQuestions = []codexQualityQuestion{
	{"logic", "All zorps are nims. No nim is a vax. Can any zorp be a vax? Return JSON {\"answer\":false} or {\"answer\":true}.", `false`},
	{"logic", "A box has 3 red and 2 blue balls. Two are drawn without replacement. Probability both are red? Return JSON answer as a reduced fraction string.", `"3/10"`},
	{"logic", "Exactly one of A and B is true. B and C have the same truth value. C is true. Return JSON answer containing the boolean value of A.", `false`},
	{"code", "Python 3: a=[1,2]; b=a; a=a+[3]; b.append(4). What is (a,b)? Return JSON answer as [a,b].", `[[1,2,3],[1,2,4]]`},
	{"code", "JavaScript: let n=0; const f=()=>n++; const a=[f(),f(),n]; Return JSON answer containing a.", `[0,1,2]`},
	{"code", "Python 3: def f(x, seen=None):\n if seen is None: seen=[]\n seen.append(x)\n return seen\nWhat is [f(1),f(2)]? Return JSON answer.", `[[1],[2]]`},
	{"tool", "Produce a tool request for lookup_weather with city Paris and unit celsius. Return JSON answer equal to an object with exactly name and arguments; arguments has exactly city and unit.", `{"name":"lookup_weather","arguments":{"city":"Paris","unit":"celsius"}}`},
	{"tool", "The tool returns {\"items\":[{\"id\":9,\"active\":false},{\"id\":3,\"active\":true},{\"id\":7,\"active\":true}]}. Return JSON answer as the sorted ascending IDs of active items.", `[3,7]`},
	{"tool", "Schema: {\"name\":\"add\",\"arguments\":{\"x\":integer,\"y\":integer}}. Construct a tool call adding -2 and 5, with no extra fields. Return the call as JSON answer.", `{"name":"add","arguments":{"x":-2,"y":5}}`},
	{"context", "Facts in order: shipment A goes to Rome; shipment B goes to Oslo; A changes destination to Lima; B remains unchanged. Return JSON answer mapping A and B to their final cities.", `{"A":"Lima","B":"Oslo"}`},
	{"context", "Remember code K=R7, code M=T2. A later record updates K to P4. A quotation says \"M=ZZ\" but quotations are not updates. Return JSON answer as [K,M].", `["P4","T2"]`},
	{"context", "Instructions: keep only the last explicit color update for each object. Records: cup=blue; plate=white; cup=green; question: is plate red?; cup=black. Questions do not update values. Return JSON answer as {cup,plate}.", `{"cup":"black","plate":"white"}`},
}

type codexQualityRound struct {
	At    time.Time `json:"at"`
	Score int       `json:"score"`
}
type codexQualityState struct {
	Version        string              `json:"version"`
	StartedAt      time.Time           `json:"started_at"`
	NextAt         time.Time           `json:"next_at"`
	Index          int                 `json:"index"`
	Scores         []bool              `json:"scores"`
	Rounds         []codexQualityRound `json:"rounds"`
	Calls          []time.Time         `json:"calls"`
	Baseline       *int                `json:"baseline,omitempty"`
	Bad            int                 `json:"bad"`
	Good           int                 `json:"good"`
	LastDecisionAt time.Time           `json:"last_decision_at"`
	Isolated       bool                `json:"isolated"`
	LastResult     string              `json:"last_result"`
	TicketID       string              `json:"ticket_id,omitempty"`
}
type CodexTicketQualityStatus struct {
	Version            string             `json:"version"`
	Mode               string             `json:"mode"`
	LastResult         string             `json:"last_result"`
	NextAt             time.Time          `json:"next_at"`
	Baseline           *int               `json:"baseline,omitempty"`
	Latest             *codexQualityRound `json:"latest,omitempty"`
	Isolated           bool               `json:"isolated"`
	CompletedQuestions int                `json:"completed_questions"`
}

func codexQualityOf(a *Account, model string) codexQualityState {
	all := map[string]codexQualityState{}
	raw, _ := json.Marshal(a.Extra[codexTicketQualityKey])
	_ = json.Unmarshal(raw, &all)
	q := all[model]
	if q.Version != codexQualityVersion {
		return codexQualityState{Version: codexQualityVersion}
	}
	return q
}
func saveCodexQuality(a *Account, model string, q codexQualityState) {
	all := map[string]codexQualityState{}
	raw, _ := json.Marshal(a.Extra[codexTicketQualityKey])
	_ = json.Unmarshal(raw, &all)
	if all == nil {
		all = map[string]codexQualityState{}
	}
	if a.Extra == nil {
		a.Extra = map[string]any{}
	}
	all[model] = q
	a.Extra[codexTicketQualityKey] = all
}
func (s *OpenAIGatewayService) codexQualityDue(a *Account, model string, now time.Time) bool {
	if !s.openAICodexTicketConfig().QualityObservationEnabled {
		return false
	}
	ac := codexAccountTicketConfigOf(a)
	ticket := s.lookupOpenAICodexTicket(a, model)
	if !ticket.validFor(a, ac, now) || ticket.ExpiresAt.Before(now.Add(5*time.Minute)) {
		return false
	}
	q := codexQualityOf(a, model)
	return !q.NextAt.After(now)
}
func (s *OpenAIGatewayService) codexQualityBlocks(a *Account, model string) bool {
	return s.openAICodexTicketConfig().QualityObservationEnabled && s.openAICodexTicketConfig().QualityIsolationEnabled && codexQualityOf(a, model).Isolated
}
func codexQualityGrade(text string, question codexQualityQuestion) (bool, bool) {
	var got map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &got) != nil || len(got) != 1 {
		return false, true
	}
	answer, ok := got["answer"]
	if !ok {
		return false, true
	}
	var actual, expected any
	if json.Unmarshal(answer, &actual) != nil || json.Unmarshal([]byte(question.Answer), &expected) != nil {
		return false, true
	}
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(expected)
	return bytes.Equal(a, b), true
}
func codexQualityComplete(q *codexQualityState, now time.Time, isolation bool) {
	score := 0
	for _, correct := range q.Scores {
		if correct {
			score++
		}
	}
	q.Rounds = append(q.Rounds, codexQualityRound{now, score})
	q.Index = 0
	q.Scores = nil
	q.LastResult = "valid"
	if q.Baseline == nil && len(q.Rounds) >= 3 && now.Sub(q.StartedAt) >= 72*time.Hour {
		first := []int{q.Rounds[0].Score, q.Rounds[1].Score, q.Rounds[2].Score}
		sort.Ints(first)
		base := first[1]
		q.Baseline = &base
		q.LastDecisionAt = now
	} else if q.Baseline != nil && isolation && (q.LastDecisionAt.IsZero() || now.Sub(q.LastDecisionAt) >= 15*time.Minute) {
		q.LastDecisionAt = now
		if score <= *q.Baseline-3 {
			q.Bad++
			q.Good = 0
			if q.Bad >= 2 {
				q.Isolated = true
			}
		} else if score >= *q.Baseline-1 {
			q.Good++
			q.Bad = 0
			if q.Good >= 2 {
				q.Isolated = false
			}
		} else {
			q.Bad = 0
			q.Good = 0
		}
	}
	if len(q.Rounds) > 12 {
		q.Rounds = q.Rounds[len(q.Rounds)-12:]
	}
}

type codexProbeKindKey struct{}
type codexProbePayloadKey struct{}
type codexProbeCaptureKey struct{}

func codexQualityResponseText(raw []byte) string {
	parse := func(data []byte) string {
		root := gjson.ParseBytes(data)
		typ := root.Get("type").String()
		if typ == "response.completed" || typ == "response.done" {
			root = root.Get("response")
		} else if root.Get("object").String() != "response" {
			return ""
		}
		var text strings.Builder
		for _, output := range root.Get("output").Array() {
			for _, part := range output.Get("content").Array() {
				if part.Get("type").String() == "output_text" {
					_, _ = text.WriteString(part.Get("text").String())
				}
			}
		}
		return text.String()
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return parse(raw)
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	text := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			if next := parse([]byte(strings.TrimSpace(line[5:]))); next != "" {
				text = next
			}
		}
	}
	return text
}
func (s *OpenAIGatewayService) runCodexQualityJob(ctx context.Context, id int64, job *codexAccountTicketJob) {
	finalState := "failed"
	defer func() {
		finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.mutateCodexTicket(finish, id, 0, func(a *Account, _ int, now time.Time) (bool, error) {
			rt := codexTicketRuntimes(a)[job.model]
			if rt.LeaseToken != job.leaseToken {
				return false, nil
			}
			rt.LeaseToken = ""
			rt.LeaseUntil = nil
			rt.Phase = "idle"
			rt.Task.finish(finalState, now)
			rt.RequestedProxyID = ""
			activateCodexPendingManual(&rt)
			saveCodexTicketRuntime(a, job.model, rt)
			return true, nil
		})
		s.openaiCodexAccountMu.Lock()
		job.running = false
		s.openaiCodexAccountMu.Unlock()
	}()
	for attempt := job.startAttempt; attempt < 3; attempt++ {
		if !s.openAICodexTicketEnabledContext(ctx) {
			finalState = "cancelled"
			return
		}
		blocked := false
		var question codexQualityQuestion
		var q codexQualityState
		account, err := s.mutateCodexTicket(ctx, id, 0, func(a *Account, _ int, now time.Time) (bool, error) {
			rt := codexTicketRuntimes(a)[job.model]
			ac := codexAccountTicketConfigOf(a)
			if !codexTicketLeaseMatches(rt, job, now) || !ac.manages(job.model) || ac.Revision != job.revision || codexTicketFixedProxyFingerprint(a) != job.fixedFingerprint {
				return false, ErrCodexTicketConflict
			}
			if cooldown := codexTicketAccountRetryAfter(a, now); cooldown != nil {
				return false, ErrCodexTicketConflict
			}
			if reason, _ := iqHealth(a, now); reason != "" {
				return false, ErrCodexTicketConflict
			}
			q = codexQualityOf(a, job.model)
			if q.StartedAt.IsZero() {
				q.StartedAt = now
			}
			recent := q.Calls[:0]
			for _, at := range q.Calls {
				if at.After(now.Add(-24 * time.Hour)) {
					recent = append(recent, at)
				}
			}
			q.Calls = recent
			if len(q.Calls) >= 36 {
				q.NextAt = q.Calls[0].Add(24 * time.Hour)
				saveCodexQuality(a, job.model, q)
				blocked = true
				return true, nil
			}
			if q.Index < 0 || q.Index >= len(codexQualityQuestions) {
				q.Index = 0
				q.Scores = nil
			}
			question = codexQualityQuestions[q.Index]
			q.NextAt = now.Add(6 * time.Hour)
			saveCodexQuality(a, job.model, q)
			until := now.Add(2 * time.Minute)
			rt.LeaseUntil = &until
			saveCodexTicketRuntime(a, job.model, rt)
			return true, nil
		})
		if err != nil || blocked {
			return
		}
		ticket := s.lookupOpenAICodexTicket(account, job.model)
		if !ticket.validFor(account, codexAccountTicketConfigOf(account), time.Now()) {
			return
		}
		reservation, err := s.reserveCodexBudget(ctx, id, job.model, 1, "quality")
		if err != nil {
			return
		}
		token, _, err := s.GetAccessToken(ctx, account)
		if err != nil {
			s.releaseCodexBudget(id, reservation)
			return
		}
		payload, _ := json.Marshal(map[string]any{"model": job.model, "store": false, "stream": true, "instructions": "Return only the requested JSON object with the single key answer.", "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": question.Prompt}}}}})
		var captured []byte
		qualityCtx := context.WithValue(ctx, codexProbeKindKey{}, "quality")
		probeCtx := context.WithValue(context.WithValue(context.WithValue(qualityCtx, codexBudgetContextKey{}, reservation), codexProbePayloadKey{}, payload), codexProbeCaptureKey{}, &captured)
		state, status, probeErr := s.fireCodexAccountTicketProbe(probeCtx, account, token, job.model, account.Proxy.URL(), ticket.State, 25*time.Second)
		s.releaseCodexBudget(id, reservation)
		correct, valid := false, false
		if probeErr == nil && status == http.StatusOK && (len(state) != 312 || !validCodexTicketState(state)) {
			answer := codexQualityResponseText(captured)
			if strings.TrimSpace(answer) != "" {
				correct, valid = codexQualityGrade(answer, question)
			}
		}
		_, err = s.mutateCodexTicket(ctx, id, 0, func(a *Account, _ int, now time.Time) (bool, error) {
			rt := codexTicketRuntimes(a)[job.model]
			if !codexTicketLeaseMatches(rt, job, now) || codexAccountTicketConfigOf(a).Revision != job.revision || codexTicketFixedProxyFingerprint(a) != job.fixedFingerprint {
				return false, ErrCodexTicketConflict
			}
			current := s.lookupOpenAICodexTicket(a, job.model)
			if !receiptForCodexTicket(ticket).matches(current) {
				return false, ErrCodexTicketConflict
			}
			q.Calls = codexQualityOf(a, job.model).Calls
			q.LastResult = "unknown"
			q.TicketID = receiptForCodexTicket(ticket).identity()
			q.NextAt = now.Add(6 * time.Hour)
			if valid {
				q.LastResult = "valid"
				q.Scores = append(q.Scores, correct)
				q.Index++
				if q.Index == len(codexQualityQuestions) {
					codexQualityComplete(&q, now, s.openAICodexTicketConfig().QualityIsolationEnabled)
				}
			}
			if q.Isolated {
				q.NextAt = now.Add(15 * time.Minute)
			}
			saveCodexQuality(a, job.model, q)
			var rejection *codexTicketProbeError
			sharedRejection := errors.As(probeErr, &rejection) && rejection.stop && rejection.account
			if status == 401 || status == 403 || status == 429 || sharedRejection {
				next := now.Add(5 * time.Minute)
				var pe *codexTicketProbeError
				if errors.As(probeErr, &pe) && pe.retryAt.After(next) {
					next = pe.retryAt
				}
				if rt.AccountRetryAfter == nil || rt.AccountRetryAfter.Before(next) {
					rt.AccountRetryAfter = &next
				}
				saveCodexTicketRuntime(a, job.model, rt)
			}
			return true, nil
		})
		if err != nil {
			return
		}
		outcome := "unknown"
		if valid {
			outcome = "incorrect"
			if correct {
				outcome = "correct"
			}
		}
		s.traceCodexTicket(CodexTicketTraceEvent{AccountID: id, Model: job.model, Stage: "quality", Outcome: outcome, Detail: CodexTicketTraceDetail{TaskID: job.taskID, TicketID: q.TicketID, HTTPStatus: status, Source: question.Category}})
		if !valid {
			finalState = "unchanged"
			return
		}
	}
	finalState = "succeeded"
}

// Limit captures used for objective grading; ordinary business observation stays streaming.
func captureCodexProbeBody(body io.Reader, target *[]byte) (io.Reader, error) {
	raw, err := io.ReadAll(io.LimitReader(body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 2<<20 {
		return nil, io.ErrShortBuffer
	}
	*target = raw
	return bytes.NewReader(raw), nil
}
