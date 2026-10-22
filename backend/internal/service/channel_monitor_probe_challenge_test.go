//go:build unit

package service

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

var probeChallengePromptRegex = regexp.MustCompile(`^Reply with only the number\. (\d+) \+ (\d+) = \?$`)

func TestGenerateChallenge_ZeroShotAdditionWithinRange(t *testing.T) {
	for i := 0; i < 500; i++ {
		c := generateChallenge()
		m := probeChallengePromptRegex.FindStringSubmatch(c.Prompt)
		require.Len(t, m, 3, "prompt %q", c.Prompt)
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		require.GreaterOrEqual(t, a, 1)
		require.LessOrEqual(t, a, 20)
		require.GreaterOrEqual(t, b, 1)
		require.LessOrEqual(t, b, 20)
		require.Equal(t, strconv.Itoa(a+b), c.Expected)
	}
}

func TestProbeAnswerFound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		final bool
		want  bool
	}{
		{"answer at tail may still grow", "40", false, false},
		{"answer at tail when text is final", "40", true, true},
		{"answer followed by punctuation", "40.", false, true},
		{"answer inside a sentence", "The answer is 40 because", false, true},
		{"prefix of a longer number", "4", false, false},
		{"longer number", "400", true, false},
		{"negative number", "-40", true, false},
		{"wrong first, right later", "41, no: 40!", false, true},
		{"echoed question with answer", "23 + 17 = 40", true, true},
		{"empty", "", true, false},
		{"think block holds the answer", "<think>23+17=40</think>41", true, false},
		{"answer after think block", "<think>23+17=40</think>\n40", true, true},
		{"unclosed think block", "<think>40", true, false},
		{"orphan close tag hides reasoning", "so 40</think>41", true, false},
		{"thinking tag variant", "<Thinking>40</Thinking>41", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, probeAnswerFound(stripThinkBlocks(tc.text), "40", tc.final))
		})
	}
}

func TestValidateChallenge_IgnoresThinkBlocks(t *testing.T) {
	require.True(t, validateChallenge("<think>maybe 41</think>40", "40"))
	require.False(t, validateChallenge("<think>40</think>41", "40"))
}
