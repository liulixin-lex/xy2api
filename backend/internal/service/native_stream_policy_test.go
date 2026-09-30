package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNativeStreamPolicySnapshotIsImmutable(t *testing.T) {
	p := NativeStreamPolicy{Version: 7, Delivery: true, Recovery: true}
	ctx := WithNativeStreamPolicy(context.Background(), p)
	p.Delivery = false
	require.True(t, NativeStreamDeliveryEnabled(ctx))
	require.EqualValues(t, 7, NativeStreamPolicyFromContext(ctx).Version)
	require.False(t, NativeStreamDeliveryEnabled(context.Background()))
}

func TestNativeStreamPolicyMiddlewareUsesAuthenticatedGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := int64(42)
	r.Use(func(c *gin.Context) { c.Set("api_key", &APIKey{ID: 9, GroupID: &group}) })
	r.Use(NativeStreamPolicyMiddleware(func(ctx context.Context, id int64) (NativeStreamPolicy, error) {
		require.EqualValues(t, 42, id)
		return NativeStreamPolicy{Version: 8, Delivery: true}, nil
	}))
	r.GET("/responses", func(c *gin.Context) {
		require.True(t, NativeStreamDeliveryEnabled(c.Request.Context()))
		require.EqualValues(t, 8, NativeStreamPolicyFromContext(c.Request.Context()).Version)
		c.Status(http.StatusNoContent)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/responses", nil))
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestNativeStreamPolicyRefreshOnlyChangesNewTurn(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	version := int64(1)
	source := nativeStreamPolicyReader{groupID: 42, read: func(ctx context.Context, group int64) (NativeStreamPolicy, error) {
		require.EqualValues(t, 42, group)
		require.NoError(t, ctx.Err())
		return NativeStreamPolicy{Version: version, Delivery: version == 1}, nil
	}}
	base := context.WithValue(parent, nativeStreamPolicyReaderKey{}, source)
	old, err := RefreshNativeStreamPolicy(base)
	require.NoError(t, err)
	version = 2
	next, err := RefreshNativeStreamPolicy(old)
	require.NoError(t, err)
	require.True(t, NativeStreamDeliveryEnabled(old))
	require.False(t, NativeStreamDeliveryEnabled(next))
	require.EqualValues(t, 1, NativeStreamPolicyFromContext(old).Version)
	require.EqualValues(t, 2, NativeStreamPolicyFromContext(next).Version)
	cancel()
	require.ErrorIs(t, next.Err(), context.Canceled)
}

func TestNativeStreamPolicyRefreshFailureDoesNotMutateSnapshot(t *testing.T) {
	ctx := WithNativeStreamPolicy(context.Background(), NativeStreamPolicy{Version: 7, Delivery: true})
	ctx = context.WithValue(ctx, nativeStreamPolicyReaderKey{}, nativeStreamPolicyReader{read: func(context.Context, int64) (NativeStreamPolicy, error) {
		return NativeStreamPolicy{}, context.DeadlineExceeded
	}})
	next, err := RefreshNativeStreamPolicy(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, ctx, next)
	require.EqualValues(t, 7, NativeStreamPolicyFromContext(next).Version)
}
