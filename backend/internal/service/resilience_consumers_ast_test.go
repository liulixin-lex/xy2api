//go:build unit

package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func productionResilienceCallsites(t *testing.T, method string, budgetDeadlineOnly bool) map[string]int {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	backendRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	internalRoot := filepath.Join(backendRoot, "internal")
	fset := token.NewFileSet()
	callsites := map[string]int{}
	err := filepath.WalkDir(internalRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(backendRoot, path)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			receiver := ""
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				receiver = astTypeName(fn.Recv.List[0].Type) + "."
			}
			key := filepath.ToSlash(rel) + ":" + receiver + fn.Name.Name
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != method {
					return true
				}
				if budgetDeadlineOnly && !isBudgetDeadlineReceiver(selector.X) {
					return true
				}
				callsites[key]++
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return callsites
}

func astTypeName(expr ast.Expr) string {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.StarExpr:
		return astTypeName(node.X)
	case *ast.IndexExpr:
		return astTypeName(node.X)
	default:
		return "?"
	}
}

func isBudgetDeadlineReceiver(expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name == "b" || node.Name == "budget"
	case *ast.CallExpr:
		selector, ok := node.Fun.(*ast.SelectorExpr)
		return ok && selector.Sel.Name == "Budget"
	default:
		return false
	}
}

func sortedCallsiteKeys(callsites map[string]int) []string {
	keys := make([]string, 0, len(callsites))
	for key, count := range callsites {
		keys = append(keys, key+"="+strconv.Itoa(count))
	}
	sort.Strings(keys)
	return keys
}

func TestBudgetDeadlineConsumerASTU10(t *testing.T) {
	want := map[string]map[string]int{
		"CanAttempt": {
			"internal/handler/failover_loop.go:FailoverState.HandleSelectionExhausted": 1,
			"internal/handler/resilience_budget.go:resilienceAttemptFailed":            2,
			"internal/handler/resilience_recovery_outcome.go:recordSelectionExhausted": 1,
		},
		"ConsumeInPlaceRetry": {
			"internal/service/antigravity_gateway_retry.go:AntigravityGatewayService.antigravityRetryLoop":      3,
			"internal/service/gemini_messages_compat_service.go:geminiInPlaceRetryAllowed":                      1,
			"internal/service/openai_gateway_forward.go:OpenAIGatewayService.Forward":                           1,
			"internal/service/openai_m3.go:openAIM3Attempt.learn":                                               1,
			"internal/service/openai_messages_continuation.go:resilienceContinuationReplayAllowed":              1,
			"internal/service/resilience_budget.go:resilienceInServiceRetryAllowed":                             1,
			"internal/service/resilience_request_repair_replay.go:newOpenAIRejectedFieldRepairSignal":           1,
			"internal/service/resilience_request_repair_replay.go:newOpenAIRequestRepairSignal":                 1,
			"internal/service/resilience_web_search_replay.go:OpenAIGatewayService.openAIWebSearchReplayDecide": 1,
		},
		"Remaining": {
			"internal/handler/resilience_budget.go:resilienceSlotWaitTimeout":       1,
			"internal/resilience/budget.go:Budget.ClampWait":                        1,
			"internal/resilience/request_timing.go:RequestState.NoteOpenAIDispatch": 1,
			"internal/resilience/timeout_plan.go:RequestState.ResponseStagePlan":    1,
		},
		"ClampWait": {
			"internal/handler/resilience_budget.go:resilienceRetryDelay":                               1,
			"internal/handler/resilience_budget.go:sameAccountRetryAllowedCtx":                         1,
			"internal/service/antigravity_gateway_retry.go:AntigravityGatewayService.handleSmartRetry": 1,
			"internal/service/gemini_messages_compat_service.go:sleepGeminiBackoffCtx":                 1,
		},
		// AttemptTimeout has no production callsites; callers use TimeoutPlan.
		"AttemptTimeout": {},
		// Deadline is read once for the non-gating request timing observation.
		"Deadline": {"internal/resilience/request_timing.go:RequestState.Timing": 1},
	}
	for _, method := range []string{"CanAttempt", "ConsumeInPlaceRetry", "Remaining", "ClampWait", "AttemptTimeout", "Deadline"} {
		got := productionResilienceCallsites(t, method, method == "Deadline")
		if !reflect.DeepEqual(got, want[method]) {
			t.Errorf("%s callsites changed; review the §2.2.1 consumer list before updating this AST guard\n got: %s\nwant: %s", method, strings.Join(sortedCallsiteKeys(got), ", "), strings.Join(sortedCallsiteKeys(want[method]), ", "))
		}
	}
}

func TestAttemptProfileCallsitesASTU12(t *testing.T) {
	got := productionResilienceCallsites(t, "SetAttemptProfile", false)
	want := map[string]int{
		"internal/handler/openai_chat_completions.go:OpenAIGatewayHandler.ChatCompletions":                                          1,
		"internal/handler/openai_gateway_handler.go:OpenAIGatewayHandler.Messages":                                                  1,
		"internal/handler/openai_gateway_handler.go:OpenAIGatewayHandler.Responses":                                                 1,
		"internal/handler/openai_images.go:OpenAIGatewayHandler.Images":                                                             1,
		"internal/service/gateway_anthropic_passthrough.go:GatewayService.forwardAnthropicAPIKeyPassthroughWithInput":               1,
		"internal/service/gateway_convert_stream.go:GatewayService.armGatewayConvertFirstEvent":                                     1,
		"internal/service/gateway_forward.go:GatewayService.Forward":                                                                4,
		"internal/service/gateway_forward_as_chat_completions.go:GatewayService.ForwardAsChatCompletions":                           1,
		"internal/service/gateway_forward_as_responses.go:GatewayService.ForwardAsResponses":                                        1,
		"internal/service/gemini_chat_completions_compat_service.go:GeminiMessagesCompatService.forwardClaudeBodyAsChatCompletions": 1,
		"internal/service/gemini_messages_compat_service.go:GeminiMessagesCompatService.Forward":                                    1,
		"internal/service/gemini_messages_compat_service.go:GeminiMessagesCompatService.ForwardNative":                              1,
		"internal/service/openai_m3.go:resetOpenAIRepairAttempt":                                                                    1,
		"internal/service/resilience_gemini_attempt.go:newGeminiAttempt":                                                            1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SetAttemptProfile callsites changed; classify each new site and its timer ownership (image handler is intentionally exempt)\n got: %s\nwant: %s", strings.Join(sortedCallsiteKeys(got), ", "), strings.Join(sortedCallsiteKeys(want), ", "))
	}
}

func TestOpenAIRepairAttemptTimerOwnersASTU12(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	backendRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	serviceRoot := filepath.Join(backendRoot, "internal", "service")
	want := map[string]int{
		"internal/service/openai_gateway_forward.go:OpenAIGatewayService.Forward":                      1,
		"internal/service/openai_gateway_passthrough.go:OpenAIGatewayService.forwardOpenAIPassthrough": 1,
	}
	got := map[string]int{}
	fset := token.NewFileSet()
	for name := range want {
		fileName := filepath.Join(serviceRoot, filepath.Base(strings.SplitN(name, ":", 2)[0]))
		file, err := parser.ParseFile(fset, fileName, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(backendRoot, fileName)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			reset := callPositions(fn.Body, "resetOpenAIRepairAttempt")
			if len(reset) == 0 {
				continue
			}
			if fn.Recv == nil || len(fn.Recv.List) == 0 {
				t.Fatalf("resetOpenAIRepairAttempt caller has no receiver: %s", fn.Name.Name)
			}
			identity := filepath.ToSlash(rel) + ":" + astTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
			got[identity] += len(reset)
			plans := callPositions(fn.Body, "openAIFirstOutputPlanFor")
			guards := callPositions(fn.Body, "newOpenAIFirstOutputHeaderGuard")
			if len(reset) != 1 || len(plans) == 0 || len(guards) == 0 || !(reset[0] < plans[0] && plans[0] < guards[0]) {
				t.Errorf("%s must choose each replay start before its plan and header guard", identity)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resetOpenAIRepairAttempt timer owners changed; update the explicit U12 disposition\n got: %s\nwant: %s", strings.Join(sortedCallsiteKeys(got), ", "), strings.Join(sortedCallsiteKeys(want), ", "))
	}
}

func callPositions(node ast.Node, name string) []token.Pos {
	var positions []token.Pos
	ast.Inspect(node, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == name {
				positions = append(positions, call.Pos())
			}
		case *ast.SelectorExpr:
			if fun.Sel.Name == name {
				positions = append(positions, call.Pos())
			}
		}
		return true
	})
	return positions
}
