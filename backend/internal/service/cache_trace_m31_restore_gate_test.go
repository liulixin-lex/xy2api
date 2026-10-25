package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// M3.1 FX-L01-1: restoreOpenAIUpstreamIdentityForClient records the restore
// fallback into the trace, so the ingress structure test treats it as a trace
// hook. Its only gate is openAIUpstreamIdentityResponseApplied, which must be
// false and allocation-free whenever L-01 is not enabled, even if a stale or
// forged wire value is present in the gin context.
func TestCacheTraceM31RestoreGateFalseWhenL01Disabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.False(t, CachePolicyFromGin(c).UpstreamIdentity().Enabled())
	require.False(t, openAIUpstreamIdentityResponseApplied(c, account))
	c.Set(openAIUpstreamIdentityWireGinKey, openAIUpstreamIdentityWire{accountID: account.ID, uuid: "00000000-0000-4000-8000-000000000000"})
	require.False(t, openAIUpstreamIdentityResponseApplied(c, account), "policy gate precedes the wire lookup")
	require.Zero(t, testing.AllocsPerRun(100, func() { _ = openAIUpstreamIdentityResponseApplied(c, account) }))
}

// This boundary feeds every Chat success/failed/cyber converter from one
// restored reader. It must remain after the HTTP-error branch and under the
// actual write result; a policy-only gate is insufficient.
func TestCacheTraceM31ChatRestoreStructure(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "openai_gateway_chat_completions.go", nil, 0)
	require.NoError(t, err)
	count := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		calls := cacheTraceUsageASTCalls(fn.Body, "restoreOpenAIUpstreamIdentityChatBody")
		for _, call := range calls {
			count++
			require.Equal(t, "forwardAsChatCompletions", fn.Name.Name)
			require.Equal(t, []string{"c", "resp.Body"}, cacheTraceUsageASTArgs(call.Args))
			parents := cacheTraceUsageASTParents(fn.Body)
			var gate *ast.IfStmt
			for child := ast.Node(call); child != nil; child = parents[child] {
				if candidate, ok := parents[child].(*ast.IfStmt); ok {
					require.Same(t, candidate.Body, child)
					gate = candidate
					break
				}
			}
			require.NotNil(t, gate)
			if gate != nil {
				require.Equal(t, []string{"identityChatApplied"}, cacheTraceUsageASTArgs([]ast.Expr{gate.Cond}))
			}
			handlers := append(cacheTraceUsageASTCalls(fn.Body, "handleChatStreamingResponse"), cacheTraceUsageASTCalls(fn.Body, "handleChatBufferedStreamingResponse")...)
			require.Len(t, handlers, 2)
			for _, handler := range handlers {
				require.Less(t, call.Pos(), handler.Pos())
			}
			rejections := cacheTraceUsageASTCalls(fn.Body, "handleOpenAIUpstreamIdentityRejection")
			require.Len(t, rejections, 1)
			require.Less(t, rejections[0].Pos(), call.Pos(), "HTTP errors keep their existing restoration path")
		}
	}
	require.Equal(t, 1, count, "exactly one registered converter-body boundary")
}
