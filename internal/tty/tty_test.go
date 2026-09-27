package tty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// keys replays key presses, then blocks until ctx is done or fails with err.
type keys struct {
	presses []string
	err     error
}

func (k *keys) ReadKey(ctx context.Context) ([]byte, error) {
	if len(k.presses) > 0 {
		p := k.presses[0]
		k.presses = k.presses[1:]
		return []byte(p), nil
	}
	if k.err != nil {
		return nil, k.err
	}
	<-ctx.Done()
	return nil, fmt.Errorf("read key: %w", ctx.Err())
}

var fullOptions = []acp.PermissionOption{
	{OptionId: "allow", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
	{OptionId: "always", Name: "Always allow", Kind: acp.PermissionOptionKindAllowAlways},
	{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
	{OptionId: "never", Name: "Never", Kind: acp.PermissionOptionKindRejectAlways},
}

func request(opts []acp.PermissionOption) acp.RequestPermissionRequest {
	return acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{ToolCallId: "t1", Title: acp.Ptr("rm -rf build\nrm -rf dist")},
		Options:  opts,
	}
}

func optionID(o acp.RequestPermissionOutcome) string {
	if o.Selected == nil {
		return "<none>"
	}
	return string(o.Selected.OptionId)
}

func TestPermissionKeys(t *testing.T) {
	onlyAllow := fullOptions[:2]
	rejectAlways := []acp.PermissionOption{fullOptions[0], fullOptions[3]}
	tests := []struct {
		name    string
		opts    []acp.PermissionOption
		presses []string
		want    string
	}{
		{"digit", fullOptions, []string{"2"}, "always"},
		{"digit out of range ignored", fullOptions[:3], []string{"4", "3"}, "reject"},
		{"y", fullOptions, []string{"y"}, "allow"},
		{"n", fullOptions, []string{"n"}, "reject"},
		{"Esc", fullOptions, []string{"\x1b"}, "reject"},
		{"Esc falls back to reject always", rejectAlways, []string{"\x1b"}, "never"},
		{"Enter is not a default", fullOptions, []string{"\r", "\n", " ", "n"}, "reject"},
		{"arrow key is not Esc", fullOptions, []string{"\x1b[A", "1"}, "allow"},
		{"n without a reject option", onlyAllow, []string{"n", "1"}, "allow"},
		{"several keys in one read: the first that decides", fullOptions, []string{"\r2", "n"}, "always"},
		{"arrow key then a digit in one read", fullOptions, []string{"\x1b[B3", "1"}, "allow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			got, err := Permission(t.Context(), &keys{presses: tt.presses}, &out, request(tt.opts), Style{})
			if err != nil || optionID(got) != tt.want {
				t.Fatalf("Permission = %s, %v; want %s", optionID(got), err, tt.want)
			}
		})
	}
}

func TestPermissionOutput(t *testing.T) {
	var out strings.Builder
	req := request(fullOptions[:3])
	req.ToolCall.Locations = []acp.ToolCallLocation{{Path: "/src/build"}}
	if _, err := Permission(t.Context(), &keys{presses: []string{"1"}}, &out, req, Style{}); err != nil {
		t.Fatal(err)
	}
	want := "⚠ Allow? rm -rf build\r\n  rm -rf dist\r\n" +
		"  /src/build\r\n" +
		"  1 Allow once 2 Always allow 3 Reject  · Esc reject · ^C cancel\r\n" +
		"  → Allow once\r\n"
	if out.String() != want {
		t.Errorf("output\n%q\nwant\n%q", out.String(), want)
	}
}

func TestPermissionInterrupt(t *testing.T) {
	_, err := Permission(t.Context(), &keys{presses: []string{"\x03"}}, io.Discard, request(fullOptions), Style{})
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v, want ErrInterrupted", err)
	}
}

func TestPermissionContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Permission(ctx, &keys{}, io.Discard, request(fullOptions), Style{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A dialog that cannot read keys rejects, or fails if it cannot.
func TestPermissionReadError(t *testing.T) {
	got, err := Permission(t.Context(), &keys{err: io.EOF}, io.Discard, request(fullOptions), Style{})
	if err != nil || optionID(got) != "reject" {
		t.Fatalf("Permission = %s, %v; want reject", optionID(got), err)
	}
	if _, err := Permission(t.Context(), &keys{err: io.EOF}, io.Discard, request(fullOptions[:2]), Style{}); err == nil {
		t.Fatal("no error without a reject option")
	}
	if got, err := Reject(request(fullOptions)); err != nil || optionID(got) != "reject" {
		t.Fatalf("Reject = %s, %v", optionID(got), err)
	}
}
