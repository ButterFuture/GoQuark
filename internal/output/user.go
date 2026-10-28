package output

// userFields are the account fields safe to print or hand to an agent. The
// account endpoints also return live credentials (pus, sToken, kfToken,
// mobilekps, …), so a field not listed here is dropped rather than shown.
var userFields = map[string]bool{
	"nickname": true, "name": true, "avatar": true, "avatar_url": true, "avatarUri": true,
	"uid": true, "uidu": true, "originUid": true, "wsgUid": true, "user_id": true,
	"mobile": true, "phone": true, "security_mobile": true, "email": true,
	"member_type": true, "super_vip_exp_at": true,
	"use_capacity": true, "total_capacity": true, "timestamp": true,
}

// PublicUser returns the displayable account fields of info.
func PublicUser(info map[string]any) map[string]any {
	if info == nil {
		return nil
	}
	out := make(map[string]any, len(userFields))
	for k, v := range info {
		if userFields[k] {
			out[k] = v
		}
	}
	return out
}
