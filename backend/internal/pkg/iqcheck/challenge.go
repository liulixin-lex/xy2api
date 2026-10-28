package iqcheck

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// R and S always use apple, peach, watermelon order. Cols and Rows describe
// display order; Draw always contains round then star counts.
type Challenge struct {
	V     int    `json:"v"`
	R     [3]int `json:"r"`
	S     [3]int `json:"s"`
	Cols  [3]int `json:"cols"`
	Rows  [2]int `json:"rows"`
	Draw  [2]int `json:"draw"`
	Naive int    `json:"naive"`
}

func NewChallenge() Challenge {
	c := Challenge{V: 1, Cols: [3]int{0, 1, 2}, Rows: [2]int{0, 1}}
	for i := range c.R {
		c.R[i], c.S[i] = 4+rand.IntN(6), 4+rand.IntN(6)
	}
	rand.Shuffle(len(c.Cols), func(i, j int) { c.Cols[i], c.Cols[j] = c.Cols[j], c.Cols[i] })
	rand.Shuffle(len(c.Rows), func(i, j int) { c.Rows[i], c.Rows[j] = c.Rows[j], c.Rows[i] })
	c.Draw, c.Naive = c.solution()
	return c
}

func (c Challenge) solution() ([2]int, int) {
	ra, rp, rw := c.R[0], c.R[1], c.R[2]
	sa, sp, sw := c.S[0], c.S[1], c.S[2]
	choices := [4][2]int{
		{rw + 1, max(sa, sp) + sw + 1},
		{max(ra, rp) + rw + 1, sw + 1},
		{rp + rw + 1, sa + sw + 1},
		{ra + rw + 1, sp + sw + 1},
	}
	draw := choices[0]
	for _, next := range choices[1:] {
		if next[0]+next[1] < draw[0]+draw[1] {
			draw = next
		}
	}
	return draw, rw + sw + max(sa+sp, rp+sp, ra+sa, ra+rp) + 1
}

func (c Challenge) Answer() int {
	draw, _ := c.solution()
	return draw[0] + draw[1]
}

func (c Challenge) Prompt() string {
	const intro = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）`
	flavors := [3]string{"苹果味", "桃子味", "西瓜味"}
	shapes := [2]string{"圆形", "五角星形"}
	counts := [2][3]int{c.R, c.S}
	var b strings.Builder
	_, _ = b.WriteString(intro)
	fmt.Fprintf(&b, "\n%s %s %s", flavors[c.Cols[0]], flavors[c.Cols[1]], flavors[c.Cols[2]])
	for _, row := range c.Rows {
		fmt.Fprintf(&b, "\n%s %d %d %d", shapes[row], counts[row][c.Cols[0]], counts[row][c.Cols[1]], counts[row][c.Cols[2]])
	}
	_, _ = b.WriteString("\n只输出一个 JSON 对象，仅含整数 answer，不输出解释或 Markdown。")
	return b.String()
}
