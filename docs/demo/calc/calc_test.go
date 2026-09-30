package calc

import "testing"

func TestEval(t *testing.T) {
	for _, c := range []struct {
		name, expr string
		want       float64
	}{
		{"addition", "2 + 3", 5},
		{"precedence", "2 + 3 * 4", 14},
		{"division", "8 / 2", 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Eval(Parse(c.expr)); got != c.want {
				t.Errorf("Eval(%q) = %v, want %v", c.expr, got, c.want)
			}
		})
	}
}
