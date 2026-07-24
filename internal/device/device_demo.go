//go:build demo

package device

import (
	"time"

	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/demobuild"
)

// ensureDemoDevice writes a host-stable fake Mac identity into demo config only.
// Re-applies if machine_id is missing or not the demo-derived id (keeps demos consistent).
func ensureDemoDevice(cfg *config.Config, nameOverride string) error {
	seed := demobuild.HostSeed()
	mid := demobuild.FakeMachineID(seed)
	id, marketing, arch := demobuild.FakeModel(seed)
	nick := demobuild.FakeNickname(seed)
	name := nameOverride
	if name == "" {
		// Official-like: "MacBook Pro demo"
		name = clipPart(marketing + " " + nick)
	} else {
		name = clipPart(name)
	}
	want := config.Device{
		MachineID:  mid,
		DeviceName: name,
		Model:      id,
		OSVersion:  demobuild.FakeOSVersion(seed),
		Arch:       arch,
		Platform:   "darwin",
		CreatedAt:  time.Unix(1700000000+seed%100000, 0).UTC().Format(time.RFC3339),
	}
	if cfg.HasDevice() &&
		cfg.Device.MachineID == want.MachineID &&
		cfg.Device.DeviceName == want.DeviceName &&
		cfg.Device.Model == want.Model {
		return nil
	}
	cfg.Device = want
	return cfg.Save()
}
