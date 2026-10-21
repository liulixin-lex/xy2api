package service

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
)

// monitorChallengePromptTemplate 零样本一句话加法题。推理模型对简单题想得更少，
// 不再带 few-shot 示例（XY2API 渠道监控 v1 探测优化）。
const monitorChallengePromptTemplate = "Reply with only the number. %d + %d = ?"

// monitorChallengeInstruction Responses 的 instructions、Anthropic 的 system 只留这一句。
const monitorChallengeInstruction = "Reply with only the number."

// monitorChallengeNumberRegex 提取响应中的所有整数（含负号）。
var monitorChallengeNumberRegex = regexp.MustCompile(`-?\d+`)

// monitorThinkOpenRegex / monitorThinkCloseRegex 正文里的推理标签（<think>、<thinking>）。
var (
	monitorThinkOpenRegex  = regexp.MustCompile(`(?i)<think(?:ing)?>`)
	monitorThinkCloseRegex = regexp.MustCompile(`(?i)</think(?:ing)?>`)
)

// monitorChallenge 一次 challenge 的 prompt + 期望答案。
type monitorChallenge struct {
	Prompt   string
	Expected string
}

// generateChallenge 生成一次随机加法题：两个 [monitorChallengeMin, monitorChallengeMax]
// 整数相加。仍然随机，防缓存。
//
// 不强求加密随机：math/rand/v2 足够分散，避免 crypto/rand 的开销。
func generateChallenge() monitorChallenge {
	a := randIntInRange(monitorChallengeMin, monitorChallengeMax)
	b := randIntInRange(monitorChallengeMin, monitorChallengeMax)
	return monitorChallenge{
		Prompt:   fmt.Sprintf(monitorChallengePromptTemplate, a, b),
		Expected: strconv.Itoa(a + b),
	}
}

// randIntInRange 返回 [min, max] 闭区间的随机整数。
func randIntInRange(minVal, maxVal int) int {
	if maxVal <= minVal {
		return minVal
	}
	return minVal + rand.IntN(maxVal-minVal+1) //nolint:gosec
}

// validateChallenge 在完整的响应正文中查找 expected 整数答案，返回是否通过校验。
// <think> 标签内的内容不参与校验。
func validateChallenge(responseText, expected string) bool {
	return probeAnswerFound(stripThinkBlocks(responseText), expected, true)
}

// probeAnswerFound 在正文 visible 中查找独立出现的整数 expected。
// 末尾的数字可能还没收完（"4" 之后还会来 "0"），所以只有后面紧跟非数字字符，
// 或 final（正文已经完整）时才算找到。
func probeAnswerFound(visible, expected string, final bool) bool {
	if visible == "" || expected == "" {
		return false
	}
	for _, loc := range monitorChallengeNumberRegex.FindAllStringIndex(visible, -1) {
		if visible[loc[0]:loc[1]] != expected {
			continue
		}
		if loc[1] < len(visible) || final {
			return true
		}
	}
	return false
}

// stripThinkBlocks 去掉正文中的推理标签及其内容：未闭合的开标签隐藏其后全部内容；
// 部分部署省略开标签、只输出闭标签，此时闭标签之前的内容都视为推理。
func stripThinkBlocks(text string) string {
	if !strings.Contains(text, "<") {
		return text
	}
	rest := text
	open := monitorThinkOpenRegex.FindStringIndex(rest)
	if closing := monitorThinkCloseRegex.FindStringIndex(rest); closing != nil && (open == nil || closing[0] < open[0]) {
		rest = rest[closing[1]:]
	}
	var b strings.Builder
	for {
		open = monitorThinkOpenRegex.FindStringIndex(rest)
		if open == nil {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:open[0]])
		rest = rest[open[1]:]
		closing := monitorThinkCloseRegex.FindStringIndex(rest)
		if closing == nil {
			return b.String()
		}
		rest = rest[closing[1]:]
	}
}
