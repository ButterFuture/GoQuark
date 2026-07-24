//go:build !demo

package demobuild

import "time"

func HostSeed() int64                         { return 0 }
func FakeMachineID(seed int64) string         { return "" }
func FakeNickname(seed int64) string          { return "" }
func FakeModel(seed int64) (string, string, string) {
	return "", "", ""
}
func FakeOSVersion(seed int64) string         { return "" }
func FakeUserID(seed int64) string            { return "" }
func FakeNicknameCN(seed int64) string        { return "" }
func FakeEmail(seed int64) string             { return "" }
func FakeCapacity(seed int64) (int64, int64)  { return 0, 0 }
func RandomWait5to10(seed int64) time.Duration { return 0 }
