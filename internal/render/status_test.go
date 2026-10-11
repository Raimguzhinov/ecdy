package render

import (
	"fmt"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func TestStatus(t *testing.T) {
	const clr = "\r\x1b[2K"
	tests := []struct {
		name  string
		o     Options
		act   func(r *Renderer, w *strings.Builder) []bool
		drawn []bool
		want  string
	}{
		{
			name: "redrawn, then erased before the text",
			o:    Options{Rewrite: true},
			act: func(r *Renderer, _ *strings.Builder) []bool {
				a, b := r.Status("waiting 2s"), r.Status("waiting 3s")
				r.Update(acp.UpdateAgentMessageText("hi"))
				return []bool{a, b, r.Status("waiting 4s")}
			},
			drawn: []bool{true, true, false},
			want:  clr + "waiting 2s" + clr + "waiting 3s" + clr + "hi\n",
		},
		{
			name:  "dimmed and cut to the width",
			o:     Options{Rewrite: true, Color: true, Width: func() int { return 9 }},
			act:   func(r *Renderer, _ *strings.Builder) []bool { return []bool{r.Status("waiting for claude")} },
			drawn: []bool{true},
			want:  clr + "\x1b[2mwaiting…\x1b[0m" + clr,
		},
		{
			name: "erased before a tool call",
			o:    Options{Rewrite: true},
			act: func(r *Renderer, _ *strings.Builder) []bool {
				a := r.Status("s")
				r.Update(readMain)
				return []bool{a, r.Status("s")}
			},
			drawn: []bool{true, false},
			want:  clr + "s" + clr + "⚙ Read main.go\n",
		},
		{
			name: "an update that shows nothing keeps it",
			o:    Options{Rewrite: true},
			act: func(r *Renderer, _ *strings.Builder) []bool {
				a := r.Status("s")
				r.Update(acp.UpdateAgentThoughtText("hmm"))
				return []bool{a, r.Status("t")}
			},
			drawn: []bool{true, true},
			want:  clr + "s" + clr + "t" + clr,
		},
		{
			name: "ends at a dialog",
			o:    Options{Rewrite: true},
			act: func(r *Renderer, w *strings.Builder) []bool {
				a := r.Status("s")
				resume := r.Pause()
				w.WriteString("dialog\n")
				resume()
				return []bool{a, r.Status("t")}
			},
			drawn: []bool{true, false},
			want:  clr + "s" + clr + "dialog\n",
		},
		{
			name: "ends at stderr",
			o:    Options{Rewrite: true},
			act: func(r *Renderer, w *strings.Builder) []bool {
				a := r.Status("s")
				r.Hide()
				w.WriteString("stderr\n")
				return []bool{a, r.Status("t")}
			},
			drawn: []bool{true, false},
			want:  clr + "s" + clr + "stderr\n",
		},
		{
			name: "ends at a notice",
			o:    Options{Rewrite: true},
			act: func(r *Renderer, _ *strings.Builder) []bool {
				a := r.Status("s")
				r.Notice("^C")
				return []bool{a, r.Status("t")}
			},
			drawn: []bool{true, false},
			want:  clr + "s" + clr + "^C\n",
		},
		{
			name: "erased by Finish",
			o:    Options{Rewrite: true},
			act: func(r *Renderer, w *strings.Builder) []bool {
				a := r.Status("s")
				r.Finish()
				w.WriteString("ecdy: error\n")
				return []bool{a}
			},
			drawn: []bool{true},
			want:  clr + "s" + clr + "ecdy: error\n",
		},
		{
			name:  "never on a pipe",
			act:   func(r *Renderer, _ *strings.Builder) []bool { return []bool{r.Status("s")} },
			drawn: []bool{false},
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			r := New(&b, tt.o)
			drawn := tt.act(r, &b)
			r.Pad()
			if b.String() != tt.want {
				t.Errorf("output %q, want %q", b.String(), tt.want)
			}
			if fmt.Sprint(drawn) != fmt.Sprint(tt.drawn) {
				t.Errorf("Status returned %v, want %v", drawn, tt.drawn)
			}
		})
	}
}
