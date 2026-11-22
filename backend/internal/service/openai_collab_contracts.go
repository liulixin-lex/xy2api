package service

import (
	"encoding/binary"
	"maps"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// CollabPlan separates names that identify historical calls from the current
// state that authorizes marker synthesis. History alone never authorizes it.
// The legacy planner/restorer keep their existing maps until separately wired.
type CollabPlan struct {
	HistoricalTools       map[string]struct{}
	EffectiveTools        map[string]CollabToolState
	DeveloperTextRewrites int
}

type CollabToolType string

const (
	CollabToolTypeFunction CollabToolType = "function"
	CollabToolTypeCustom   CollabToolType = "custom"
	CollabToolTypeUnknown  CollabToolType = "unknown"
)

type CollabToolState struct {
	Available            bool
	Type                 CollabToolType
	HasMessage           bool
	MessageMarkerRemoved bool
	ExtraEncrypted       bool
}

func (p CollabPlan) Clone() CollabPlan {
	p.HistoricalTools = maps.Clone(p.HistoricalTools)
	p.EffectiveTools = maps.Clone(p.EffectiveTools)
	return p
}

// CollabDigest reuses the existing server secret shared across instances.
// It never falls back to an unkeyed hash or a process-random key. Parts use
// length-unambiguous byte framing; message bytes must be passed unchanged.
// An unavailable secret produces absence. No caller logs inputs or the secret.
func (s *OpenAIGatewayService) CollabDigest(domain resilience.CollabDigestDomain, parts ...string) resilience.CollabDigest {
	switch domain {
	case resilience.CollabDigestDomainRejection, resilience.CollabDigestDomainBody, resilience.CollabDigestDomainThread, resilience.CollabDigestDomainTree,
		resilience.CollabDigestDomainParent, resilience.CollabDigestDomainDelivery,
		resilience.CollabDigestDomainItem, resilience.CollabDigestDomainCiphertext,
		resilience.CollabDigestDomainArguments, resilience.CollabDigestDomainCall,
		resilience.CollabDigestDomainAccount, resilience.CollabDigestDomainSite,
		resilience.CollabDigestDomainEvent, resilience.CollabDigestDomainScope:
	default:
		return ""
	}
	var framed []byte
	for _, part := range parts {
		framed = binary.BigEndian.AppendUint64(framed, uint64(len(part)))
		framed = append(framed, part...)
	}
	return resilience.CollabDigest(s.cacheIdentityDigest("collab-v1/"+string(domain), string(framed)))
}
