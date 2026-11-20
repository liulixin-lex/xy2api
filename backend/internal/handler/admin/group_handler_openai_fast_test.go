package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupRequestsDecodeForceOpenAIFast(t *testing.T) {
	var createReq CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"fast","force_openai_fast":true,"free_openai_fast":true}`), &createReq))
	require.NotNil(t, createReq.ForceOpenAIFast)
	require.True(t, *createReq.ForceOpenAIFast)
	require.True(t, createReq.FreeOpenAIFast)

	var updateReq UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"force_openai_fast":false,"free_openai_fast":false}`), &updateReq))
	require.NotNil(t, updateReq.ForceOpenAIFast)
	require.False(t, *updateReq.ForceOpenAIFast)
	require.NotNil(t, updateReq.FreeOpenAIFast)
	require.False(t, *updateReq.FreeOpenAIFast)

	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.Nil(t, omitted.ForceOpenAIFast)
	require.Nil(t, omitted.FreeOpenAIFast)
}

func TestGroupPricingDecodeUltrafastMultiplierPresence(t *testing.T) {
	var req UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model_pricing":[{"models":["a"]},{"models":["b"],"ultrafast_multiplier":null},{"models":["c"],"ultrafast_multiplier":2.5}]}`), &req))
	require.NotNil(t, req.ModelPricing)
	pricing := groupModelPricingFromRequest(*req.ModelPricing)
	require.False(t, pricing[0].UltrafastMultiplierSet)
	require.True(t, pricing[1].UltrafastMultiplierSet)
	require.Nil(t, pricing[1].UltrafastMultiplier)
	require.True(t, pricing[2].UltrafastMultiplierSet)
	require.NotNil(t, pricing[2].UltrafastMultiplier)
	require.Equal(t, 2.5, *pricing[2].UltrafastMultiplier)
}
