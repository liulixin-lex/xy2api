package service

import (
	"math"
	"time"
)

var cacheCounterPlatforms = [...]string{"openai", "anthropic", "gemini"}

// Existing callers retain the OpenAI partition. Provider hooks must pass the
// actual platform, after their request snapshot has admitted the observation.
func (c *CacheTraceCounters) RecordPlatformBusiness(now time.Time, platform string, prompt, read int64, host, session, model string, account int64) {
	if partition := c.platformPartition(platform, true); partition != nil {
		partition.RecordBusiness(now, prompt, read, host, session, model, account)
	}
}

func (c *CacheTraceCounters) RecordPlatformTrace(now time.Time, platform string, data CacheTraceCounterSample) {
	if partition := c.platformPartition(platform, true); partition != nil {
		partition.recordTraceSample(now, data)
	}
}

// Child partitions are bounded and lazy. Each owns the original minute/hour,
// host and session limits; provider traffic cannot evict OpenAI state.
func (c *CacheTraceCounters) platformPartition(platform string, create bool) *CacheTraceCounters {
	if c == nil {
		return nil
	}
	if platform == PlatformOpenAI {
		return c
	}
	index := -1
	switch platform {
	case PlatformAnthropic:
		index = 0
	case PlatformGemini:
		index = 1
	default:
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	partition := c.providers[index]
	if partition == nil && create {
		partition = &CacheTraceCounters{
			started: c.started, processID: c.processID,
			hosts: map[string]cacheCounterHost{}, sessions: map[cacheCounterSessionKey]cacheCounterSession{},
		}
		c.providers[index] = partition
	}
	return partition
}

func cacheCounterScope(policy *CachePolicySnapshot, query string) ([]string, []string) {
	mask := uint8(cacheTracePlatformOpenAI)
	if policy != nil && policy.platforms != 0 {
		mask = policy.platforms
	}
	covered := []string{}
	for _, platform := range cacheCounterPlatforms {
		bit := cacheTracePlatformBit(platform)
		if platform == PlatformOpenAI {
			bit |= cacheTracePlatformMessagesBridge
		}
		if mask&bit != 0 && (query == "all" || query == platform) {
			covered = append(covered, platform)
		}
	}
	openAI, provider := false, false
	for _, platform := range covered {
		openAI = openAI || platform == PlatformOpenAI
		provider = provider || platform != PlatformOpenAI
	}
	notes := []string{}
	if openAI && mask&cacheTracePlatformMessagesBridge == 0 {
		notes = append(notes, "messages_bridge_excluded")
	}
	if openAI && mask&cacheTracePlatformOpenAI == 0 {
		notes = append(notes, "openai_native_excluded")
	}
	if query != "all" && len(covered) == 0 {
		notes = append(notes, "platform_not_covered")
	}
	if query != PlatformOpenAI && (query != "all" || provider) {
		notes = append(notes, "openai_only_metrics")
	}
	return covered, notes
}

func (c *CacheTraceCounters) PreviousHour(now time.Time) []CacheTraceHour {
	out := []CacheTraceHour{}
	for _, platform := range cacheCounterPlatforms {
		if partition := c.platformPartition(platform, false); partition != nil {
			out = append(out, partition.previousPlatformHour(now, platform)...)
		}
	}
	return out
}

func (c *CacheTraceCounters) partitionSnapshot(now, start time.Time) (cacheCounterMinute, map[string]cacheCounterHost) {
	total := cacheCounterMinute{dimensions: map[string]map[string]cacheCounterValue{}}
	hosts := map[string]cacheCounterHost{}
	if c == nil {
		return total, hosts
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, bucket := range c.minutes {
		if bucket == nil || bucket.at.Before(start.Truncate(time.Minute)) || bucket.at.After(now) {
			continue
		}
		addCacheCounterTotals(&total, bucket)
		for dimension, values := range bucket.dimensions {
			for key, value := range values {
				total.add(dimension, key, value)
			}
		}
	}
	for host, value := range c.hosts {
		hosts[host] = value
	}
	return total, hosts
}

func addCacheCounterTotals(total, part *cacheCounterMinute) {
	total.business.add(part.business)
	total.trace.add(part.trace)
	total.known += part.known
	total.uuid += part.uuid
	total.pairs += part.pairs
	total.switches += part.switches
	for i := range total.headersIn {
		total.headersIn[i] += part.headersIn[i]
		total.headersOut[i] += part.headersOut[i]
	}
}

type cacheCounterMergedHost struct {
	original     cacheCounterHost
	latest       time.Time
	prompt, read float64
	samples      int64
	partitions   int
}

func (c *CacheTraceCounters) selectedSnapshot(now, start time.Time, selected []string) (cacheCounterMinute, cacheCounterMinute, map[string]cacheCounterHost) {
	total := cacheCounterMinute{dimensions: map[string]map[string]cacheCounterValue{}}
	openAI := cacheCounterMinute{dimensions: map[string]map[string]cacheCounterValue{}}
	merged := map[string]cacheCounterMergedHost{}
	for _, platform := range selected {
		partition := c.platformPartition(platform, false)
		part, hosts := partition.partitionSnapshot(now, start)
		if platform == PlatformOpenAI {
			openAI = part
		}
		addCacheCounterTotals(&total, &part)
		// Each input already obeys its partition's 128-key bound. Summation
		// must not apply that cap again across platforms or fold before sum.
		for dimension, values := range part.dimensions {
			if total.dimensions[dimension] == nil {
				total.dimensions[dimension] = map[string]cacheCounterValue{}
			}
			for key, value := range values {
				previous := total.dimensions[dimension][key]
				previous.add(value)
				total.dimensions[dimension][key] = previous
			}
		}
		for host, value := range hosts {
			sum := merged[host]
			if sum.partitions == 0 {
				sum.original = value
			}
			if value.at.After(sum.latest) {
				sum.latest = value.at
			}
			decay := math.Exp(-math.Ln2 * math.Max(0, now.Sub(value.at).Seconds()) / 1800)
			sum.prompt += value.prompt * decay
			sum.read += value.read * decay
			sum.samples += value.samples
			sum.partitions++
			merged[host] = sum
		}
	}
	hosts := make(map[string]cacheCounterHost, len(merged))
	for host, sum := range merged {
		// A single partition keeps the previous arithmetic bit-for-bit.
		if sum.partitions == 1 {
			hosts[host] = sum.original
		} else {
			hosts[host] = cacheCounterHost{at: sum.latest, prompt: sum.prompt, read: sum.read, samples: sum.samples}
		}
	}
	return total, openAI, hosts
}
