package main

import (
	"net/http"
	"strconv"
	"strings"
)

// messages holds all user-visible strings, keyed by language then key.
// Supported languages: "zh" (Simplified Chinese), "en" (English, the fallback).
var messages = map[string]map[string]string{
	"en": {
		// nav
		"nav.terminal": "Terminal",
		"nav.files":    "Files",
		"nav.changepw": "Change password",
		"nav.logout":   "Log out",
		"nav.home":     "Home",

		// status dashboard
		"status.now":           "Current time:",
		"status.cpu":           "CPU usage",
		"status.mem":           "Memory",
		"status.mempct":        "Memory usage",
		"status.disk":          "Disk",
		"status.diskfree":      "Disk free",
		"status.used":          "used",
		"status.timeline":      "72-hour uptime timeline",
		"status.legend_hint":   "Unknown = no data",
		"status.events":        "Offline events",
		"status.ev_start":      "Start",
		"status.ev_kind":       "Type",
		"status.ev_end":        "End",
		"status.ev_dur":        "Duration",
		"status.no_events":     "No outages detected in the past 72 hours 🎉",
		"status.footer":        "Heartbeat recorded every minute · Page auto-refreshes every 60 seconds · Data retained for 72 hours · Hover over the timeline for details · Times shown in your browser's local timezone",
		"state.active":         "Running",
		"state.offline":        "Offline",
		"state.unknown":        "Unknown",
		"event.offline":        "Offline",
		"event.reboot":         "Offline (rebooted)",

		// login
		"login.title":     "Sign in",
		"login.sub":       "Please sign in to continue",
		"login.username":  "Username",
		"login.password":  "Password",
		"login.submit":    "Sign in",
		"login.changed":   "Password changed. Please sign in with your new password.",
		"login.secure":    "Muse Console · Secure sign-in",
		"login.err_bad":   "Invalid username or password",
		"login.err_limit": "Too many attempts, try again in %d minutes",

		// change password
		"changepw.title":        "Change password",
		"changepw.sub":          "You'll need to sign in again with the new password",
		"changepw.old":          "Current password",
		"changepw.new":          "New password (min 6 characters)",
		"changepw.confirm":      "Confirm new password",
		"changepw.submit":       "Confirm",
		"changepw.back":         "← Back to home",
		"changepw.err_mismatch": "New passwords don't match",
		"changepw.err_wrongold": "Current password is incorrect",
		"changepw.err_short":    "New password must be at least 6 characters",

		// shared form error
		"form.err_invalid": "Invalid request",

		// change password via Basic Auth (plain-text HTTP error)
		"changepw.err_cookie": "Please sign in via the login page first",

		// terminal
		"term.title":  "Terminal",
		"term.header": "Muse Console · Terminal",

		// file browser
		"files.title":        "Files",
		"files.upload":       "Upload",
		"files.mkdir":        "New folder",
		"files.refresh":      "Refresh",
		"files.th_name":      "Name",
		"files.th_size":      "Size",
		"files.th_mtime":     "Modified",
		"files.th_act":       "Actions",
		"files.jail_note":    "Paths are jailed to the file root; `..` escapes are rejected.",
		"files.close":        "Close",
		"files.root":         "Root",
		"files.empty":        "Empty folder",
		"files.load_fail":    "Failed to load: ",
		"files.download":     "Download",
		"files.preview":      "Preview",
		"files.rename":       "Rename",
		"files.delete":       "Delete",
		"files.img_fail":     "Failed to load image",
		"files.nopreview":    "Preview not supported for this type. Please download to view.",
		"files.truncated":    " (first 256KB only)",
		"files.rename_to":    "Rename to:",
		"files.rename_fail":  "Rename failed: ",
		"files.del_confirm":  "Delete \"%s\"?",
		"files.del_confirmd": " (folder and its contents)",
		"files.delete_fail":  "Delete failed: ",
		"files.mkdir_name":   "Folder name:",
		"files.mkdir_fail":   "Failed to create: ",
		"files.up_partial":   "Some files failed to upload",
		"files.up_fail":      "Upload failed: ",

		// file API errors
		"api.path_escape":  "path escapes file root",
		"api.cannot_list":  "cannot list directory",
		"api.not_file":     "not a file",
		"api.cannot_open":  "cannot open file",
		"api.not_text":     "not a text file",
		"api.post_only":    "POST only",
		"api.not_dir":      "target is not a directory",
		"api.cannot_parse": "cannot parse upload: ",
		"api.missing_path": "missing path",
		"api.del_root":     "cannot delete file root",
		"api.missing_ft":   "missing from/to",
	},
	"zh": {
		// nav
		"nav.terminal": "终端",
		"nav.files":    "文件",
		"nav.changepw": "改密码",
		"nav.logout":   "退出",
		"nav.home":     "首页",

		// status dashboard
		"status.now":           "当前时间：",
		"status.cpu":           "CPU 使用率",
		"status.mem":           "内存",
		"status.mempct":        "内存使用率",
		"status.disk":          "磁盘",
		"status.diskfree":      "磁盘剩余",
		"status.used":          "已用",
		"status.timeline":      "过去 72 小时运行时间线",
		"status.legend_hint":   "未知 = 无数据",
		"status.events":        "异常事件（离线）",
		"status.ev_start":      "开始",
		"status.ev_kind":       "类型",
		"status.ev_end":        "结束",
		"status.ev_dur":        "时长",
		"status.no_events":     "过去 72 小时内没有检测到离线 🎉",
		"status.footer":        "每分钟记录一次心跳 · 页面每 60 秒自动刷新 · 数据保留 72 小时 · 把鼠标悬停在时间条上可查看每段详情 · 时间按浏览器本地时区显示",
		"state.active":         "运行中",
		"state.offline":        "离线",
		"state.unknown":        "未知",
		"event.offline":        "离线",
		"event.reboot":         "离线（机器重启）",

		// login
		"login.title":     "登录",
		"login.sub":       "请登录以继续",
		"login.username":  "账号",
		"login.password":  "密码",
		"login.submit":    "登录",
		"login.changed":   "密码已修改，请用新密码重新登录",
		"login.secure":    "Muse Console · 安全登录",
		"login.err_bad":   "用户名或密码错误",
		"login.err_limit": "尝试次数过多，请 %d 分钟后再试",

		// change password
		"changepw.title":        "修改密码",
		"changepw.sub":          "修改后需要用新密码重新登录",
		"changepw.old":          "当前密码",
		"changepw.new":          "新密码（至少 6 位）",
		"changepw.confirm":      "确认新密码",
		"changepw.submit":       "确认修改",
		"changepw.back":         "← 返回首页",
		"changepw.err_mismatch": "两次输入的新密码不一致",
		"changepw.err_wrongold": "当前密码错误",
		"changepw.err_short":    "新密码至少 6 位",

		// shared form error
		"form.err_invalid": "请求无效",

		// change password via Basic Auth (plain-text HTTP error)
		"changepw.err_cookie": "请先通过页面登录",

		// terminal
		"term.title":  "终端",
		"term.header": "Muse Console · 终端",

		// file browser
		"files.title":        "文件",
		"files.upload":       "上传",
		"files.mkdir":        "新建文件夹",
		"files.refresh":      "刷新",
		"files.th_name":      "名称",
		"files.th_size":      "大小",
		"files.th_mtime":     "修改时间",
		"files.th_act":       "操作",
		"files.jail_note":    "路径限制在文件根目录内，`..` 跳出会被拒绝。",
		"files.close":        "关闭",
		"files.root":         "根目录",
		"files.empty":        "空文件夹",
		"files.load_fail":    "加载失败：",
		"files.download":     "下载",
		"files.preview":      "预览",
		"files.rename":       "重命名",
		"files.delete":       "删除",
		"files.img_fail":     "图片加载失败",
		"files.nopreview":    "该类型不支持预览，请下载查看",
		"files.truncated":    "（仅显示前 256KB）",
		"files.rename_to":    "重命名为：",
		"files.rename_fail":  "重命名失败：",
		"files.del_confirm":  "确定删除「%s」？",
		"files.del_confirmd": "（文件夹及内容）",
		"files.delete_fail":  "删除失败：",
		"files.mkdir_name":   "文件夹名称：",
		"files.mkdir_fail":   "创建失败：",
		"files.up_partial":   "部分文件上传失败",
		"files.up_fail":      "上传失败：",

		// file API errors
		"api.path_escape":  "路径越出文件根目录",
		"api.cannot_list":  "无法列出目录",
		"api.not_file":     "不是文件",
		"api.cannot_open":  "无法打开文件",
		"api.not_text":     "不是文本文件",
		"api.post_only":    "仅支持 POST",
		"api.not_dir":      "目标不是目录",
		"api.cannot_parse": "无法解析上传：",
		"api.missing_path": "缺少路径",
		"api.del_root":     "不能删除根目录",
		"api.missing_ft":   "缺少 from/to",
	},
}

// T returns the message for lang and key, falling back to English,
// then to the key itself if missing everywhere.
func T(lang, key string) string {
	if m, ok := messages[lang]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	if s, ok := messages["en"][key]; ok {
		return s
	}
	return key
}

// htmlLang maps our language code to an HTML lang attribute value.
func htmlLang(lang string) string {
	if lang == "zh" {
		return "zh-CN"
	}
	return "en"
}

// detectLang picks "zh" or "en" from an Accept-Language header value.
// Any zh variant (zh-CN, zh-TW, zh-HK, …) maps to Simplified Chinese;
// anything else falls back to English.
func detectLang(header string) string {
	type langQ struct {
		tag string
		q   float64
	}
	var prefs []langQ
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag := part
		q := 1.0
		if i := strings.Index(part, ";"); i >= 0 {
			tag = strings.TrimSpace(part[:i])
			for _, kv := range strings.Split(part[i+1:], ";") {
				kv = strings.TrimSpace(kv)
				if strings.HasPrefix(kv, "q=") {
					if f, err := strconv.ParseFloat(strings.TrimPrefix(kv, "q="), 64); err == nil {
						q = f
					}
				}
			}
		}
		if tag == "" || tag == "*" {
			continue
		}
		prefs = append(prefs, langQ{strings.ToLower(tag), q})
	}
	// Highest q first; stable for equal q (browser order wins).
	for i := 0; i < len(prefs); i++ {
		for j := i + 1; j < len(prefs); j++ {
			if prefs[j].q > prefs[i].q {
				prefs[i], prefs[j] = prefs[j], prefs[i]
			}
		}
	}
	for _, p := range prefs {
		if strings.HasPrefix(p.tag, "zh") {
			return "zh"
		}
		if strings.HasPrefix(p.tag, "en") {
			return "en"
		}
	}
	return "en"
}

// langOf is a small helper for handlers.
func langOf(r *http.Request) string {
	return detectLang(r.Header.Get("Accept-Language"))
}
