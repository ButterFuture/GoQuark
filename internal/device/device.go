// Package device builds device identity for Quark PC client compatibility.
//
// Official reverse (renderer module 85297):
//
//	system_profiler SPHardwareDataType → "Model Name: MacBook Pro"
//	os.userInfo().username → "chen"
//	deviceName = `${ModelName} ${username}`  // note the SPACE
//	// fallback: `Mac ${username}`
//	// each part truncated to 20 runes
//
// Requests inject mi=deviceName; empty mi makes the server fall back to User-Agent
// (which is why device list showed "Mozilla/5.0 (Macintosh; ...").
package device

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
	mrand "math/rand"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ButterFuture/GoQuark/internal/config"
)

// Crockford base32 alphabet (OpenUTDID-style machine id).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// MacModel is Apple hardware model identifier + marketing Model Name.
type MacModel struct {
	Identifier string // e.g. MacBookPro18,3
	Name       string // e.g. "MacBook Pro"  (system_profiler "Model Name")
	Arch       string // arm64 / x86_64
}

// Marketing names match `system_profiler SPHardwareDataType` "Model Name:" field.
var macModels = []MacModel{
	{"MacBookAir10,1", "MacBook Air", "arm64"},
	{"Mac14,2", "MacBook Air", "arm64"},
	{"Mac15,12", "MacBook Air", "arm64"},
	{"Mac15,13", "MacBook Air", "arm64"},
	{"MacBookPro18,1", "MacBook Pro", "arm64"},
	{"MacBookPro18,3", "MacBook Pro", "arm64"},
	{"Mac14,7", "MacBook Pro", "arm64"},
	{"Mac14,9", "MacBook Pro", "arm64"},
	{"Mac15,3", "MacBook Pro", "arm64"},
	{"Mac15,6", "MacBook Pro", "arm64"},
	{"Mac14,3", "Mac mini", "arm64"},
	{"Mac14,12", "Mac mini", "arm64"},
	{"Mac16,10", "Mac mini", "arm64"},
	{"iMac21,1", "iMac", "arm64"},
	{"Mac15,4", "iMac", "arm64"},
	{"Mac13,1", "Mac Studio", "arm64"},
	{"Mac14,13", "Mac Studio", "arm64"},
	{"MacBookPro16,1", "MacBook Pro", "x86_64"},
	{"MacBookAir9,1", "MacBook Air", "x86_64"},
	{"Macmini8,1", "Mac mini", "x86_64"},
}

// Short usernames similar to macOS account short names / display fragments.
var cnUsernames = []string{
	"chen", "li", "wang", "zhang", "liu", "yang", "huang", "zhao", "zhou", "wu",
	"xiaoming", "xiaohong", "dapeng", "yifan", "haoran", "zihan", "yuxuan",
}
var enUsernames = []string{
	"alex", "chris", "david", "emma", "jack", "james", "kevin", "leo",
	"lisa", "lucy", "mike", "nina", "ryan", "sam", "tom", "amy", "jason", "helen",
}

var osVersions = []string{
	"13.6.7", "14.4.1", "14.5", "14.6.1", "15.0", "15.1", "15.2", "15.3.1",
}

// EnsureDevice fills config.Device if missing, or repairs a bad device_name.
// nameOverride forces a custom device name (still truncated like official).
func EnsureDevice(cfg *config.Config, nameOverride string) error {
	if cfg.HasDevice() {
		changed := false
		if nameOverride != "" && nameOverride != cfg.Device.DeviceName {
			cfg.Device.DeviceName = clipPart(nameOverride)
			changed = true
		} else if !IsPlausibleDeviceName(cfg.Device.DeviceName) {
			// Repair old "陈的MacBook Pro" style or UA-like names.
			cfg.Device.DeviceName = GenerateDeviceName(modelNameFromID(cfg.Device.Model), nil)
			changed = true
		}
		if changed {
			return cfg.Save()
		}
		return nil
	}
	rng := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	model := macModels[rng.Intn(len(macModels))]
	id, err := GenerateMachineID()
	if err != nil {
		return err
	}
	name := nameOverride
	if name == "" {
		name = GenerateDeviceName(model.Name, rng)
	} else {
		name = clipPart(name)
	}
	cfg.Device = config.Device{
		MachineID:  id,
		DeviceName: name,
		Model:      model.Identifier,
		OSVersion:  osVersions[rng.Intn(len(osVersions))],
		Arch:       model.Arch,
		Platform:   "darwin",
		CreatedAt:  time.Now().Format(time.RFC3339),
	}
	return cfg.Save()
}

// GenerateMachineID creates a 24-char Crockford base32 ID (OpenUTDID-like).
func GenerateMachineID() (string, error) {
	raw := make([]byte, 15)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return encodeCrockford(raw), nil
}

func encodeCrockford(src []byte) string {
	var n big.Int
	n.SetBytes(src)
	out := make([]byte, 24)
	base := big.NewInt(32)
	mod := new(big.Int)
	for i := 23; i >= 0; i-- {
		n.DivMod(&n, base, mod)
		out[i] = crockford[mod.Int64()]
	}
	return string(out)
}

// GenerateDeviceName matches official PC client:
//
//	`${Model Name} ${username}`  e.g. "MacBook Pro chen", "Mac mini lisa"
//
// Official truncates each part to 20 runes.
func GenerateDeviceName(modelName string, rng *mrand.Rand) string {
	if rng == nil {
		rng = mrand.New(mrand.NewSource(time.Now().UnixNano()))
	}
	if modelName == "" {
		modelName = "MacBook Pro"
	}
	var user string
	if rng.Intn(10) < 6 {
		user = cnUsernames[rng.Intn(len(cnUsernames))]
	} else {
		user = enUsernames[rng.Intn(len(enUsernames))]
	}
	return clipPart(modelName) + " " + clipPart(user)
}

// IsPlausibleDeviceName reports whether name looks like official "Model user" form.
func IsPlausibleDeviceName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	// UA leak
	if strings.HasPrefix(name, "Mozilla/") || strings.Contains(name, "AppleWebKit") {
		return false
	}
	// Old Chinese ComputerName style we used before: "陈的MacBook Pro"
	if strings.Contains(name, "的") {
		return false
	}
	// Official form has a space between model and user (except bare fallback "Mac user" still has space)
	if !strings.Contains(name, " ") {
		// bare "Mac mini" without user is acceptable (seen in real device list)
		return strings.HasPrefix(name, "Mac") || strings.HasPrefix(name, "iMac")
	}
	return true
}

func clipPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= 20 {
		return s
	}
	r := []rune(s)
	return string(r[:20]) + "..."
}

func modelNameFromID(id string) string {
	for _, m := range macModels {
		if m.Identifier == id {
			return m.Name
		}
	}
	return "MacBook Pro"
}

// ClientInfoString matches official getClientInfo() join format (backtick-separated).
func ClientInfoString(d config.Device) string {
	parts := []string{
		"ip:",
		"machine:",
		"imei:",
		"imsi:",
		"sn:",
		"app_name:quark-cloud-drive",
		"os:" + d.Platform,
		"mac:",
		"idfa:",
		"utdid:" + d.MachineID,
		"port:",
		"game_id:",
		"net_type:wifi",
		"client_identity:netdiskweb",
	}
	return strings.Join(parts, "`")
}

// UA builds Electron PC client User-Agent on Mac.
// Must NOT be used as device display name — that is mi/deviceName.
func UA(d config.Device) string {
	_ = d
	return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/3.24.0 Chrome/112.0.5615.165 Electron/24.0.0 Safari/537.36"
}

// SeedFromMachine returns a deterministic seed from machine id (for tests).
func SeedFromMachine(id string) int64 {
	if len(id) < 8 {
		return time.Now().UnixNano()
	}
	return int64(binary.BigEndian.Uint64([]byte(id[:8])))
}

// DebugString for logging (no secrets).
func DebugString(d config.Device) string {
	return fmt.Sprintf("%s | %s | %s", d.DeviceName, d.Model, d.MachineID)
}
