package api

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ButterFuture/GoQuark/internal/client"
)

// FileEntry is a simplified drive file row.
type FileEntry struct {
	FID      string `json:"fid"`
	Name     string `json:"file_name"`
	Size     int64  `json:"size"`
	Dir      bool   `json:"dir"`
	Updated  int64  `json:"updated_at"`
	Path     string `json:"-"`
	Raw      map[string]any
}

// UserInfo merges pan account + member capacity when available.
func UserInfo(c *client.Client) (map[string]any, error) {
	// Prefer official desktop userinfo (used by PC client).
	acc, err := c.DoJSON("GET", client.PanDomain+"/desktop/account/userinfo", nil, nil)
	if err != nil {
		// fallback older path
		acc, err = c.DoJSON("GET", client.PanDomain+"/account/info", url.Values{
			"platform": {"pc"},
		}, nil)
		if err != nil {
			return nil, err
		}
	}
	data, _ := acc["data"].(map[string]any)
	if data == nil {
		// pan userinfo sometimes returns data at top-level fields
		if success, _ := acc["success"].(bool); success {
			data = acc
		} else {
			data = map[string]any{}
		}
	}
	// member
	mem, err := c.DoJSON("GET", client.DriveDomain+"/1/clouddrive/member", url.Values{
		"fetch_subscribe": {"true"},
		"fetch_identity":  {"true"},
		"_ch":             {"home"},
	}, nil)
	if err == nil {
		if md, ok := mem["data"].(map[string]any); ok {
			for _, k := range []string{"use_capacity", "total_capacity", "member_type", "super_vip_exp_at"} {
				if v, ok := md[k]; ok {
					data[k] = v
				}
			}
		}
	} else if client.IsAuthError(err) {
		return nil, err
	}
	return data, nil
}

// ListDir lists a directory. pdirFID empty/"0" means root.
func ListDir(c *client.Client, pdirFID string, page, size int) ([]FileEntry, error) {
	if pdirFID == "" {
		pdirFID = "0"
	}
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 50
	}
	q := url.Values{}
	q.Set("pdir_fid", pdirFID)
	q.Set("_page", strconv.Itoa(page))
	q.Set("_size", strconv.Itoa(size))
	q.Set("_fetch_total", "1")
	q.Set("_fetch_sub_dirs", "0")
	q.Set("_sort", "file_type:asc,updated_at:desc")
	resp, err := c.DoJSON("GET", client.DriveDomain+"/1/clouddrive/file/sort", q, nil)
	if err != nil {
		return nil, err
	}
	if ae := authFromBody(resp); ae != nil {
		return nil, ae
	}
	if code, _ := asInt(resp["code"]); code != 0 && code != 200 {
		// non-zero business code without data
		if resp["data"] == nil {
			return nil, fmt.Errorf("list: code=%v message=%v", resp["code"], resp["message"])
		}
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil {
		return nil, fmt.Errorf("list: no data: %v", resp["message"])
	}
	list, _ := data["list"].([]any)
	out := make([]FileEntry, 0, len(list))
	for _, it := range list {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		fe := FileEntry{
			FID:  str(m["fid"]),
			Name: str(m["file_name"]),
			Size: i64(m["size"]),
			Dir:  i64(m["dir"]) == 1 || i64(m["file_type"]) == 0,
			Raw:  m,
		}
		out = append(out, fe)
	}
	return out, nil
}

// ResolvePath walks path components from root and returns fid of final node.
func ResolvePath(c *client.Client, path string) (fid string, isDir bool, err error) {
	path = cleanPath(path)
	if path == "/" || path == "" {
		return "0", true, nil
	}
	parts := splitPath(path)
	cur := "0"
	var last FileEntry
	for _, name := range parts {
		entries, err := ListDir(c, cur, 1, 200)
		if err != nil {
			return "", false, err
		}
		found := false
		for _, e := range entries {
			if e.Name == name {
				last = e
				cur = e.FID
				found = true
				break
			}
		}
		if !found {
			return "", false, fmt.Errorf("path not found: %s (missing %s)", path, name)
		}
	}
	return last.FID, last.Dir, nil
}

// GetDownloadURL requests CDN url for fid (PC download API).
func GetDownloadURL(c *client.Client, fid string) (downloadURL string, size int64, err error) {
	body := map[string]any{"fids": []string{fid}}
	resp, err := c.DoJSON("POST", client.DriveDomain+"/1/clouddrive/file/download", nil, body)
	if err != nil {
		return "", 0, err
	}
	// handle code 23018 etc
	if msg, _ := resp["message"].(string); msg != "" {
		if code := resp["code"]; fmt.Sprint(code) != "0" && code != nil && fmt.Sprint(code) != "200" {
			// still try data
		}
	}
	data := resp["data"]
	switch v := data.(type) {
	case []any:
		if len(v) == 0 {
			return "", 0, fmt.Errorf("empty download data: %v", resp)
		}
		m, _ := v[0].(map[string]any)
		return strings.TrimSpace(str(m["download_url"])), i64(m["size"]), nil
	case map[string]any:
		// sometimes map
		if list, ok := v["list"].([]any); ok && len(list) > 0 {
			m, _ := list[0].(map[string]any)
			return strings.TrimSpace(str(m["download_url"])), i64(m["size"]), nil
		}
		return strings.TrimSpace(str(v["download_url"])), i64(v["size"]), nil
	default:
		return "", 0, fmt.Errorf("unexpected download payload: %v", resp)
	}
}

func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	return p
}

func splitPath(p string) []string {
	p = cleanPath(p)
	var out []string
	cur := ""
	for _, r := range p {
		if r == '/' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func str(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func i64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(t)
		return n, err == nil
	default:
		return 0, false
	}
}

func authFromBody(resp map[string]any) error {
	if resp == nil {
		return nil
	}
	if code, ok := asInt(resp["code"]); ok && (code == 31001 || code == 31004) {
		msg, _ := resp["message"].(string)
		if msg == "" {
			msg = "请重新登录"
		}
		return &client.AuthError{Code: fmt.Sprintf("%d", code), Message: msg}
	}
	if sc, ok := resp["code"].(string); ok {
		switch sc {
		case "NO_AUTH", "AUTH_ERROR", "AUTH_ERROR:50051", "AUTH_ERROR:50052", "31001", "31004":
			msg, _ := resp["message"].(string)
			return &client.AuthError{Code: sc, Message: msg}
		}
		if strings.HasPrefix(sc, "AUTH_ERROR") {
			msg, _ := resp["message"].(string)
			return &client.AuthError{Code: sc, Message: msg}
		}
	}
	return nil
}
