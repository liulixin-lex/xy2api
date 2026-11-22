//go:build unit

package handler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// All payloads are invented here. No captured requests belong in this corpus.
const replayTask = `{"type":"agent_message","id":"amsg_task","content":[{"type":"encrypted_content","encrypted_content":"cipher-task"},{"type":"input_text","text":"task envelope"}]}`
const replayReasoning = `{"type":"reasoning","id":"rs_alpha","encrypted_content":"cipher-reasoning","summary":[{"type":"summary_text","text":"reasoning summary"}]}`
const replayCompaction = `{"type":"compaction","id":"cmp_alpha","encrypted_content":"cipher-compaction"}`
const replayOrdinary = `{"type":"message","id":"msg_alpha","role":"user","content":[{"type":"encrypted_content","encrypted_content":"cipher-ordinary"}]}`
const replayQuestion = `{"type":"message","role":"user","content":"preserve this question"}`
const replayToolOutput = `{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"synthetic tool context"}`
const replaySearch = `{"type":"web_search_call","id":"ws_alpha","status":"completed","action":{"type":"search","query":"synthetic query"}}`

type replayRule struct {
	Kind string `json:"kind"`
	Item string `json:"item,omitempty"`
}

type replayCase struct {
	Name              string            `json:"name"`
	Items             []json.RawMessage `json:"items"`
	First             []replayRule      `json:"first"`
	Second            []replayRule      `json:"second"`
	Identity          string            `json:"identity"`
	Route             string            `json:"route"`
	Form              string            `json:"form"`
	Turns             int               `json:"turns"`
	Warmup            bool              `json:"warmup,omitempty"`
	SecondUnavailable bool              `json:"second_unavailable,omitempty"`
	Pointer           bool              `json:"pointer,omitempty"`
	Duplicate         bool              `json:"duplicate,omitempty"`
	Structured        bool              `json:"structured,omitempty"`
	Stream            bool              `json:"stream"`
	Passthrough       bool              `json:"passthrough"`
	Attempts          int               `json:"attempts"`
	// A future-only precondition cannot be silently advertised as exercised.
	PendingSetup       string `json:"pending_setup,omitempty"`
	WaitBeforeRetry    bool   `json:"wait_before_retry,omitempty"`
	FirstWaiters       bool   `json:"first_waiters,omitempty"`
	SharedQuota        int    `json:"shared_quota,omitempty"`
	LittleTime         bool   `json:"little_time,omitempty"`
	ChildAfterRecovery bool   `json:"child_after_recovery,omitempty"`
	MixedMarkers       bool   `json:"mixed_markers,omitempty"`
	RecordMode         string `json:"record_mode,omitempty"`
	UserWaiters        bool   `json:"user_waiters,omitempty"`
}

func collaborationReplayCorpus() []replayCase {
	rule := func(kind, item string) replayRule { return replayRule{kind, item} }
	named := func(item string) replayRule { return rule("named", item) }
	task := []replayRule{named("amsg_task")}
	reasoning := []replayRule{named("rs_alpha")}
	all := []replayRule{rule("any_encrypted", "")}
	var seeds []replayCase
	add := func(name string, items []string, first, second []replayRule) *replayCase {
		c := replayCase{Name: name, First: first, Second: second, Identity: "root", Route: "responses", Form: "http", Turns: 1}
		for _, item := range append(items, replayQuestion) {
			c.Items = append(c.Items, json.RawMessage(item))
		}
		seeds = append(seeds, c)
		return &seeds[len(seeds)-1]
	}
	add("reasoning_only_unreadable", []string{replayTask, replayReasoning}, reasoning, all)
	add("task_readable_elsewhere", []string{replayTask, replayReasoning}, task, nil)
	add("task_then_reasoning_elsewhere", []string{replayTask, replayReasoning}, task, reasoning)
	add("summary_unreadable", []string{replayTask, replayCompaction}, []replayRule{named("cmp_alpha")}, all)
	add("child_task_local_ending_off", []string{replayTask, replayReasoning}, task, task).Identity = "child"
	add("child_task_local_ending_on", []string{replayTask, replayReasoning}, task, task).Identity = "child"
	add("root_task_unreadable", []string{replayTask}, task, task)
	add("ordinary_named_unreadable", []string{replayOrdinary, replayReasoning, replayCompaction, replayTask}, []replayRule{named("msg_alpha")}, []replayRule{named("msg_alpha")})
	add("single_attempt_task", []string{replayTask}, task, task)
	add("wrapped_once", []string{replayTask}, []replayRule{rule("wrapped_once", "")}, nil)
	add("wrapped_until_repaired", []string{replayTask}, []replayRule{rule("wrapped_encrypted", "")}, all)
	add("wrapped_never_succeeds", []string{replayTask}, []replayRule{rule("wrapped_always", "")}, []replayRule{rule("wrapped_always", "")})
	add("wrapped_insufficient_evidence", []string{replayTask, replayReasoning, replayCompaction}, []replayRule{rule("wrapped_encrypted", "")}, all)
	add("ordinary_unclaimed_error", nil, []replayRule{rule("ordinary_error", "")}, nil)
	add("successive_reasoning_rejections", []string{replayTask, replayReasoning, strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(replayReasoning, "rs_alpha", "rs_beta"), "cipher-reasoning", "cipher-second"), "reasoning summary", "second summary"), strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(replayReasoning, "rs_alpha", "rs_gamma"), "cipher-reasoning", "cipher-third"), "reasoning summary", "third summary")}, []replayRule{named("rs_alpha"), named("rs_beta"), named("rs_gamma")}, all)
	add("target_slot_unavailable", []string{replayTask}, task, nil).SecondUnavailable = true
	add("unnamed_task", []string{replayTask}, all, all)
	add("unnamed_task_reasoning_summary", []string{replayTask, replayReasoning, replayCompaction}, all, all)
	add("bad_compaction", []string{replayCompaction}, []replayRule{named("cmp_alpha")}, all)
	add("compact_task", []string{replayTask, replayReasoning}, task, task).Route = "compact"
	add("review_body", []string{replayTask}, task, task).Route = "review"
	reader := add("reader_differs_from_issuer", []string{replayTask, replayReasoning}, all, all)
	reader.Warmup = true
	reader.PendingSetup = "issuance_record"
	add("outer_gateway_inner_encrypted", []string{replayTask, replayReasoning}, task, task).Form = "outer_gateway"
	add("provenance_conflict", []string{replayTask}, []replayRule{rule("conflict", "")}, nil)
	add("provenance_rate_limit", []string{replayTask}, []replayRule{rule("rate_limit", "")}, nil)
	c := add("new_task_after_local_ending", []string{replayTask}, task, task)
	c.Turns = 2
	c.PendingSetup = "prior_local_ending"
	add("old_task_after_compaction", []string{replayTask, replayCompaction}, task, task)
	add("previous_reader_unnamed", []string{replayTask, replayReasoning}, all, all).Warmup = true
	add("switched_account_server_error", []string{replayTask}, []replayRule{rule("reject_placeholder", ""), named("amsg_task")}, []replayRule{rule("server_error", "")})
	add("next_tool_loop", []string{replayTask}, task, task).Turns = 2
	add("missing_named_item", []string{replayTask, replayReasoning}, []replayRule{rule("missing", "")}, []replayRule{rule("missing", "")})
	for _, kind := range []string{"args_accept", "args_reject", "args_placeholder_accepted", "args_plaintext"} {
		args := `{"type":"function_call","id":"fc_alpha","call_id":"call_alpha","namespace":"collaboration","name":"send_message","arguments":"{\"message\":\"cipher-arguments\"}","encrypted_function_args":["message"]}`
		if kind == "args_plaintext" {
			args = strings.ReplaceAll(strings.ReplaceAll(args, "cipher-arguments", "readable task"), `["message"]`, `[]`)
		}
		add(kind, []string{args}, []replayRule{rule(kind, "")}, nil)
		if kind != "args_plaintext" {
			add("paired_"+kind, []string{replayTask, strings.ReplaceAll(args, "cipher-arguments", "cipher-task")}, []replayRule{rule(kind, "")}, nil)
		}
	}
	add("chat_plain_collaboration", []string{`{"type":"agent_message","id":"amsg_task","content":[{"type":"input_text","text":"readable task"}]}`}, nil, nil).Route = "chat_bridge"
	for _, kind := range []string{"rate_limit", "server_error", "hang"} {
		add("target_"+kind, []string{replayTask}, task, []replayRule{rule(kind, "")})
	}
	add("second_accepts_placeholder", []string{replayTask}, []replayRule{rule("reject_placeholder", ""), named("amsg_task")}, task)
	add("long_repair_chain", []string{replayToolOutput, replaySearch, replayTask, replayReasoning}, []replayRule{rule("tool_output", ""), rule("field", "truncation"), rule("field", "max_output_tokens"), rule("web_search", ""), named("rs_alpha"), named("amsg_task")}, nil)
	add("deletion_only", []string{replayTask, replayReasoning}, []replayRule{rule("reasoning_shape", ""), named("rs_alpha")}, nil)
	retry := add("client_retry_success", []string{replayTask}, []replayRule{rule("first_turn_fails", "")}, []replayRule{rule("first_turn_fails", "")})
	retry.Turns = 2
	retry.WaitBeforeRetry = true
	add("client_immediate_retry", []string{replayTask}, []replayRule{rule("first_turn_fails", "")}, []replayRule{rule("first_turn_fails", "")}).Turns = 2
	for _, form := range []string{"failed_json", "stream_failure", "created_failure", "heartbeat_failure", "post_output_failure", "outer_gateway"} {
		add("envelope_"+form, []string{replayTask, replayReasoning}, task, task).Form = form
	}
	add("pointer_task", []string{replayTask}, task, task).Pointer = true
	add("ambiguous_identity", []string{replayTask}, task, task).Identity = "ambiguous"
	add("ambiguous_locator", []string{replayTask, replayReasoning}, []replayRule{rule("ambiguous", "")}, all)
	add("duplicate_member", []string{replayTask}, task, task).Duplicate = true
	add("structured_task", []string{replayTask}, task, task).Structured = true
	add("actual_hang", []string{replayTask}, []replayRule{rule("hang", "")}, nil)
	add("client_cancel", []string{replayTask}, []replayRule{rule("cancel", "")}, nil)
	add("first_account_waiters", []string{replayTask}, task, nil).FirstWaiters = true
	add("target_executable_then_failure", []string{replayTask}, task, []replayRule{rule("executable_failure", "")})
	add("target_created_then_stall", []string{replayTask}, task, []replayRule{rule("created_stall", "")})
	add("little_time_left", []string{replayTask}, task, []replayRule{rule("hang", "")}).LittleTime = true
	add("shared_quota_extra_send", []string{replayTask}, task, nil).SharedQuota = 2
	child := add("child_after_second_recovery", []string{replayTask}, []replayRule{rule("reject_placeholder", ""), named("amsg_task")}, task)
	child.Turns = 2
	child.ChildAfterRecovery = true
	add("single_declaration_mixed_markers", []string{replayTask}, task, task).MixedMarkers = true
	for _, mode := range []string{"slow", "failure", "panic"} {
		c := add("record_store_"+mode, []string{replayTask}, task, task)
		c.RecordMode = mode
		c.PendingSetup = "record_store_adapter"
	}
	add("executable_then_failure", []string{replayTask}, []replayRule{rule("executable_failure", "")}, nil)
	add("created_then_stall", []string{replayTask}, []replayRule{rule("created_stall", "")}, nil)
	add("queued_user_retryable", []string{replayTask}, []replayRule{rule("named_always", "amsg_task")}, []replayRule{rule("named_always", "amsg_task")}).UserWaiters = true
	add("queued_user_nonretryable", []string{replayTask}, all, all).UserWaiters = true
	add("handled_task_named_again", []string{replayTask, replayReasoning, replayCompaction}, []replayRule{rule("named_always", "amsg_task")}, []replayRule{rule("named_always", "amsg_task")})
	add("handled_reasoning_named_again", []string{replayTask, replayReasoning, replayCompaction}, []replayRule{rule("named_always", "rs_alpha")}, []replayRule{rule("named_always", "rs_alpha")})
	ordinaryAgent := strings.ReplaceAll(replayTask, "amsg_task", "amsg_ordinary")
	ordinaryAgent = strings.ReplaceAll(strings.ReplaceAll(ordinaryAgent, "cipher-task", "cipher-ordinary"), "task envelope", "ordinary envelope")
	add("ordinary_agent_named_unreadable", []string{ordinaryAgent, replayTask, replayReasoning, replayCompaction}, []replayRule{named("amsg_ordinary")}, []replayRule{named("amsg_ordinary")})
	var corpus []replayCase
	for _, seed := range seeds {
		for _, pass := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				for _, attempts := range []int{1, 2, 4, 0} {
					c := seed
					c.Passthrough = pass
					c.Stream = stream
					c.Attempts = attempts
					c.Name = fmt.Sprintf("%s/pass_%t/stream_%t/attempts_%d", seed.Name, pass, stream, attempts)
					corpus = append(corpus, c)
				}
			}
		}
	}
	return corpus
}
