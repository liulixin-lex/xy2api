//go:build unit

package service

import (
	"context"
	"testing"
)

func TestChatRequestRequirementContext(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background()} {
		if _, ok := ChatRequestRequirementFromContext(ctx); ok {
			t.Fatal("unexpected requirement")
		}
	}
	req := ChatRequestRequirement{OriginalModel: "public", DispatchModel: "mapped", ReasoningEffort: "none", HasTools: true, ToolField: "functions"}
	ctx := WithChatRequestRequirement(context.Background(), req)
	req.OriginalModel = "changed"
	got, ok := ChatRequestRequirementFromContext(ctx)
	if !ok || got.OriginalModel != "public" || got.DispatchModel != "mapped" || got.ReasoningEffort != "none" || !got.HasTools || got.ToolField != "functions" {
		t.Fatal(got, ok)
	}
	if _, ok := ChatRequestRequirementFromContext(WithChatRequestRequirement(nil, ChatRequestRequirement{})); !ok {
		t.Fatal("explicit empty requirement lost")
	}
}
