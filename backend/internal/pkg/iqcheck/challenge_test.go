package iqcheck

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func drawFeasible(c Challenge, r, s int) bool {
	ra, rp, rw := c.R[0], c.R[1], c.R[2]
	sa, sp, sw := c.S[0], c.S[1], c.S[2]
	return r <= ra+rp+rw && s <= sa+sp+sw && r > rw && s > sw && !(r <= rp+rw && s <= sp+sw) && !(r <= ra+rw && s <= sa+sw)
}

func TestChallengeClosedFormAllParameters(t *testing.T) {
	for n := 0; n < 46656; n++ {
		k := n
		c := Challenge{V: 1}
		for i := 0; i < 3; i++ {
			c.R[i] = 4 + k%6
			k /= 6
			c.S[i] = 4 + k%6
			k /= 6
		}
		c.Draw, c.Naive = c.solution()
		best := 100
		for r := 0; r <= c.R[0]+c.R[1]+c.R[2]; r++ {
			for s := 0; s <= c.S[0]+c.S[1]+c.S[2]; s++ {
				if drawFeasible(c, r, s) && r+s < best {
					best = r + s
				}
			}
		}
		if got := c.Answer(); got != best || c.Draw[0]+c.Draw[1] != got || !drawFeasible(c, c.Draw[0], c.Draw[1]) || got == c.Naive {
			t.Fatalf("%+v answer=%d brute=%d", c, got, best)
		}
	}
	c := Challenge{R: [3]int{7, 9, 8}, S: [3]int{7, 6, 4}}
	_, naive := c.solution()
	require.Equal(t, 21, c.Answer())
	require.Equal(t, 29, naive)
}

func TestChallengePromptAndJSONContract(t *testing.T) {
	c := Challenge{V: 1, R: [3]int{7, 9, 8}, S: [3]int{7, 6, 4}, Cols: [3]int{2, 0, 1}, Rows: [2]int{1, 0}, Draw: [2]int{9, 12}, Naive: 29}
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	require.JSONEq(t, `{"v":1,"r":[7,9,8],"s":[7,6,4],"cols":[2,0,1],"rows":[1,0],"draw":[9,12],"naive":29}`, string(raw))
	require.Contains(t, c.Prompt(), "西瓜味 苹果味 桃子味\n五角星形 4 7 6\n圆形 8 7 9")
}
