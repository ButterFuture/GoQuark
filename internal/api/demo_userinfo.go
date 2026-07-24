//go:build demo

package api

import (
	"fmt"
	"time"

	"github.com/ButterFuture/GoQuark/internal/demobuild"
)

func demoUserInfo() map[string]any {
	seed := demobuild.HostSeed()
	used, total := demobuild.FakeCapacity(seed)
	uid := demobuild.FakeUserID(seed)
	nick := demobuild.FakeNicknameCN(seed)
	return map[string]any{
		"nickname":         nick,
		"mobile":           maskMobile(seed),
		"email":            demobuild.FakeEmail(seed),
		"user_id":          uid,
		"kps_wg":           uid,
		"avatar_url":       "",
		"use_capacity":     used,
		"total_capacity":   total,
		"member_type":      demoMemberType(seed),
		"super_vip_exp_at": time.Now().AddDate(0, 6, int(seed%20)).UnixMilli(),
		"demo":             true,
		"demo_notice":      "goquarkdemo mock profile (not a real account)",
	}
}

func maskMobile(seed int64) string {
	// looks like a phone, not a real one
	n := 13000000000 + (seed%7000000000+7000000000)%7000000000
	s := fmt.Sprintf("%d", n)
	if len(s) >= 11 {
		return s[:3] + "****" + s[7:]
	}
	return "138****0000"
}

func demoMemberType(seed int64) string {
	types := []string{"NORMAL", "VIP", "Z_VIP", "SUPER_VIP"}
	return types[uint64(seed>>5)%uint64(len(types))]
}
