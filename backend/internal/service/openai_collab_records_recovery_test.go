//go:build unit

package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
)

func TestCollabRecoveryLazyInitialization(t *testing.T) {
	s := NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	defer s.CloseCollabRecords()
	require.Nil(t, s.collabRecords, "disabled construction must not allocate a records store")
	in := collabRecordTestInput()
	in.Settings.Collab.Records = false
	require.Zero(t, testing.AllocsPerRun(100, func() {
		require.Nil(t, s.NewCollabRecordDelivery(in))
		s.RecordCollabConsumed(context.Background(), in.Settings, true, in.Account, nil)
	}))
	require.Nil(t, s.collabRecords)
	in.Settings.Collab.Records = true
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() { require.NotNil(t, s.NewCollabRecordDelivery(in)) })
	}
	wg.Wait()
	require.NotNil(t, s.collabRecords)
}
