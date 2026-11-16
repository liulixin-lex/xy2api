//go:build integration

package repository

import "testing"

func TestSchedAttemptRepositoryV2EvidenceIntegration(t *testing.T) {
	assertSchedAttemptV2Evidence(t, integrationDB)
}
