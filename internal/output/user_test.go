package output

import "testing"

func TestPublicUserDropsCredentials(t *testing.T) {
	in := map[string]any{
		"nickname": "n", "uid": "u", "member_type": "NORMAL", "use_capacity": 1, "total_capacity": 2,
		"pus": "x", "sToken": "x", "kfToken": "x", "mobilekps": "x", "service_ticket": "x", "unknown_new_field": "x",
	}
	got := PublicUser(in)
	for _, k := range []string{"pus", "sToken", "kfToken", "mobilekps", "service_ticket", "unknown_new_field"} {
		if _, ok := got[k]; ok {
			t.Errorf("PublicUser kept %q", k)
		}
	}
	for _, k := range []string{"nickname", "uid", "member_type", "use_capacity", "total_capacity"} {
		if _, ok := got[k]; !ok {
			t.Errorf("PublicUser dropped %q", k)
		}
	}
}
