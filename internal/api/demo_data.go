//go:build demo

package api

// mockListDir: root has 9 neutral entries; every subfolder is empty.
// "来自：分享" kept; downloads are rejected in GetDownloadURL when DemoMode.
func mockListDir(pdirFID string) []FileEntry {
	if pdirFID == "" || pdirFID == "0" {
		return []FileEntry{
			{FID: "demo_dir_tender", Name: "招标文件", Dir: true, Size: 0},
			{FID: "demo_dir_project", Name: "项目资料", Dir: true, Size: 0},
			{FID: "demo_dir_share", Name: "来自：分享", Dir: true, Size: 0}, // keep
			{FID: "demo_dir_spec", Name: "技术规范", Dir: true, Size: 0},
			{FID: "demo_dir_minutes", Name: "会议纪要", Dir: true, Size: 0},
			{FID: "demo_dir_training", Name: "培训材料", Dir: true, Size: 0},
			{FID: "demo_dir_public", Name: "公共资源", Dir: true, Size: 0},
			{FID: "demo_dir_archive", Name: "归档备份", Dir: true, Size: 0},
			{FID: "demo_file_manual", Name: "产品手册_v2.pdf", Dir: false, Size: 2_458_624},
		}
	}
	// All folders open to empty listing
	return []FileEntry{}
}
