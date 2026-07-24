//go:build demo

package demobuild

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

// HostSeed returns a stable 64-bit seed for this machine (hostname + machine-id-ish env).
// Different hosts → different mock identities; same host → stable across runs.
func HostSeed() int64 {
	h := sha256.New()
	host, _ := os.Hostname()
	_, _ = h.Write([]byte(host))
	_, _ = h.Write([]byte{0})
	// Linux machine-id if present (stable per install)
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		_, _ = h.Write(bytesTrim(b))
	}
	// Distinguish from product if same host ever shared paths
	_, _ = h.Write([]byte("|goquarkdemo"))
	sum := h.Sum(nil)
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

// FakeMachineID derives a 24-char Crockford-like id from host seed (demo-only alphabet).
func FakeMachineID(seed int64) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	// mix seed
	x := uint64(seed)
	if x == 0 {
		x = 0x9e3779b97f4a7c15
	}
	var out [24]byte
	for i := 0; i < 24; i++ {
		x = x*6364136223846793005 + 1
		out[i] = alphabet[(x>>33)%32]
	}
	return string(out[:])
}

// FakeNickname picks a neutral display fragment from seed.
func FakeNickname(seed int64) string {
	names := []string{
		"demo", "guest", "office", "studio", "lab", "desk", "work", "share",
		"public", "trial", "sample", "preview",
	}
	u := uint64(seed)
	return names[u%uint64(len(names))]
}

// FakeModel returns a Mac model pair from seed.
func FakeModel(seed int64) (identifier, marketing, arch string) {
	type m struct{ id, name, arch string }
	models := []m{
		{"MacBookAir10,1", "MacBook Air", "arm64"},
		{"Mac14,2", "MacBook Air", "arm64"},
		{"MacBookPro18,3", "MacBook Pro", "arm64"},
		{"Mac14,7", "MacBook Pro", "arm64"},
		{"Mac14,3", "Mac mini", "arm64"},
		{"iMac21,1", "iMac", "arm64"},
		{"MacBookPro16,1", "MacBook Pro", "x86_64"},
	}
	u := uint64(seed >> 8)
	mm := models[u%uint64(len(models))]
	return mm.id, mm.name, mm.arch
}

// FakeOSVersion from seed.
func FakeOSVersion(seed int64) string {
	vers := []string{"14.5", "14.6.1", "15.0", "15.1", "15.2", "15.3.1"}
	return vers[uint64(seed>>16)%uint64(len(vers))]
}

// FakeUserID numeric-looking string.
func FakeUserID(seed int64) string {
	u := uint64(seed ^ 0x5a5a5a5a5a5a5a5a)
	return fmt.Sprintf("%d", 1000000000+u%8000000000)
}

// FakeNicknameCN for pan display (neutral).
func FakeNicknameCN(seed int64) string {
	ns := []string{"演示用户", "体验账号", "示例账户", "公共演示", "预览用户", "办公演示"}
	return ns[uint64(seed>>3)%uint64(len(ns))]
}

// FakeEmail-like local-only string (not a real mailbox).
func FakeEmail(seed int64) string {
	local := FakeNickname(seed)
	return fmt.Sprintf("%s_%s@example.invalid", local, hex.EncodeToString([]byte{byte(seed), byte(seed >> 8)})[:4])
}

// FakeCapacity returns used/total bytes looking like a cloud plan.
func FakeCapacity(seed int64) (used, total int64) {
	// total: 1T / 2T / 6T class
	totals := []int64{1 << 40, 2 << 40, 6 << 40, 10 << 40}
	total = totals[uint64(seed>>12)%uint64(len(totals))]
	// used 5%–35%
	pct := 5 + int(uint64(seed>>20)%31)
	used = total * int64(pct) / 100
	return used, total
}

// RandomWait5to10 returns a duration in [5s, 10s].
func RandomWait5to10(seed int64) time.Duration {
	// re-seed with time so each login waits differently, but still 5–10s
	n := time.Now().UnixNano() ^ seed
	if n < 0 {
		n = -n
	}
	sec := 5 + int(n%6) // 5..10
	return time.Duration(sec) * time.Second
}
