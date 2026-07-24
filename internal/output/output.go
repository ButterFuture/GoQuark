package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// IsAgentLike reports whether stdout is non-interactive (pipe/file) so we
// should prefer machine-readable JSON instead of human-pretty text.
func IsAgentLike() bool {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("GOQUARK_OUTPUT"))); v != "" {
		switch v {
		case "json", "agent", "machine":
			return true
		case "text", "human", "pretty":
			return false
		}
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	// not a char device → piped/redirected
	return (fi.Mode() & os.ModeCharDevice) == 0
}

// WantJSON forces JSON if --json flag or agent-like stdout / GOQUARK_OUTPUT.
func WantJSON(flagJSON bool) bool {
	if flagJSON {
		return true
	}
	return IsAgentLike()
}

// Loading prints a loading line to stderr (so agents piping stdout still get clean JSON).
// Returns a done function that clears the line when stderr is a TTY.
func Loading(msg string) (done func()) {
	if msg == "" {
		msg = "加载中…"
	}
	// Always write progress to stderr so stdout stays machine-parseable.
	fi, err := os.Stderr.Stat()
	isTTY := err == nil && (fi.Mode()&os.ModeCharDevice) != 0
	if isTTY {
		fmt.Fprint(os.Stderr, msg)
		_ = os.Stderr.Sync()
		return func() {
			// clear line with carriage return + spaces
			fmt.Fprint(os.Stderr, "\r\033[K")
		}
	}
	// non-TTY: one line then leave it (or skip). Prefer skip noise for agents.
	return func() {}
}

// PrintJSON writes indented JSON to w (default stdout).
func PrintJSON(w io.Writer, v any) error {
	if w == nil {
		w = os.Stdout
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// FormatWhoami returns human-readable account summary.
func FormatWhoami(info map[string]any, deviceName, loginWay, loggedInAt string) string {
	nick := pickStr(info, "nickname", "name")
	uid := pickStr(info, "uidu", "uid", "originUid")
	mobile := pickStr(info, "mobile", "phone", "security_mobile")
	member := pickStr(info, "member_type")
	useCap := pickI64(info, "use_capacity")
	totalCap := pickI64(info, "total_capacity")

	var b strings.Builder
	fmt.Fprintf(&b, "昵称:     %s\n", dash(nick))
	fmt.Fprintf(&b, "UID:      %s\n", dash(uid))
	if mobile != "" {
		fmt.Fprintf(&b, "手机:     %s\n", mobile)
	}
	fmt.Fprintf(&b, "会员:     %s\n", dash(member))
	if totalCap > 0 {
		fmt.Fprintf(&b, "容量:     %s / %s\n", humanSize(useCap), humanSize(totalCap))
	}
	if deviceName != "" {
		fmt.Fprintf(&b, "设备:     %s\n", deviceName)
	}
	if loginWay != "" {
		fmt.Fprintf(&b, "登录方式: %s\n", loginWay)
	}
	if loggedInAt != "" {
		fmt.Fprintf(&b, "登录时间: %s\n", loggedInAt)
	}
	return strings.TrimRight(b.String(), "\n")
}

func pickStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			s := fmt.Sprint(v)
			if s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func pickI64(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB"} {
		f /= 1024
		if f < 1024 {
			return fmt.Sprintf("%.1f %s", f, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", f/1024)
}

// Sleep is tiny helper for tests.
func Sleep(d time.Duration) { time.Sleep(d) }
