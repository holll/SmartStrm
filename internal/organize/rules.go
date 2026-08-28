// Package organize 移植自 alist_organizer：网盘目录整理（番号识别、命名规范化、整理与移动）
package organize

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ============ 番号解析规则 ============

// singleLabels 单部标签：ABC-123-c / -u / -uc / -cu 视为单部
var singleLabels = map[string]bool{"c": true, "u": true, "uc": true, "cu": true}

var avVideoRe = regexp.MustCompile(`(?i)^([a-z]{1,10}-?\d{2,5}v?)(?:-([a-z0-9]{1,10}))?`)
var avCodeRe = regexp.MustCompile(`(?i)^([a-z]{1,10})-?(\d{2,5})(v?)$`)

// specialPrefixedRe 厂商前缀含数字的特殊番号（1pondo 系 T38-068、T28-602 等）：
// 整体作为一个番号，若先走 avVideoRe 会被错拆成 T-038 + 分集 068
var specialPrefixedRe = regexp.MustCompile(`(?i)^([a-z]+\d{1,2}-\d{2,5})(?:-([a-z0-9]{1,10}))?`)

var fc2Re = regexp.MustCompile(`(?i)^(?:fc2(?:[-_\s]?ppv)?|fc1)[-_\s]?(\d{5,8})(?:-(cd\d+|\d+|[a-z]))?`)

// isCharCode 判断字符是否属于 [0-9a-zA-Z_]（含下划线）。
// 对应 Python 正向前瞻 `(?=$|[^a-z0-9_])`：匹配结束后紧跟字母/数字/下划线都视为不匹配
func isCharCode(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

// normalizeAvCode 统一 AV 番号格式：
// stars00220 -> STARS-220, ibw00184 -> IBW-184, hmn00035 -> HMN-035, abc00001 -> ABC-001
func normalizeAvCode(code string) string {
	m := avCodeRe.FindStringSubmatch(code)
	if m == nil {
		return strings.ToUpper(code)
	}
	prefix := strings.ToUpper(m[1])
	num, _ := strconv.Atoi(m[2])
	suffix := strings.ToLower(m[3])
	numS := strconv.Itoa(num)
	if num < 1000 {
		numS = fmt.Sprintf("%03d", num)
	}
	return prefix + "-" + numS + suffix
}

// parseAvVideoID 解析 AV 番号，返回 (base_code, label, is_single, ok)
// - label 为第二个 '-' 后的标签；无标签则空
// - is_single=true 表示单部（无标签或属于 c/u/uc/cu），false 表示分集
func parseAvVideoID(text string) (baseCode, label string, isSingle bool, ok bool) {
	// 优先识别厂商前缀含数字的特殊番号（T38-068 / T28-602），整体不拆分
	if loc := specialPrefixedRe.FindStringSubmatchIndex(text); loc != nil {
		if loc[1] >= len(text) || !isCharCode(text[loc[1]]) {
			base := strings.ToUpper(text[loc[2]:loc[3]])
			var lb string
			if loc[4] >= 0 {
				lb = strings.ToLower(text[loc[4]:loc[5]])
			}
			if lb == "" {
				return base, "", true, true
			}
			if singleLabels[lb] {
				return base, lb, true, true
			}
			return base, lb, false, true
		}
	}

	loc := avVideoRe.FindStringSubmatchIndex(text)
	if loc == nil {
		return "", "", false, false
	}
	// 边界：整个匹配结束后不允许再紧跟 [0-9a-z]（对应 Python 的 `(?![_]\\d)(?=$|[^a-z0-9_])`）
	if loc[1] < len(text) && isCharCode(text[loc[1]]) {
		return "", "", false, false
	}
	base := normalizeAvCode(text[loc[2]:loc[3]])
	var lb string
	if loc[4] >= 0 {
		lb = text[loc[4]:loc[5]]
		lb = strings.ToLower(lb)
	}
	if lb == "" {
		return base, "", true, true
	}
	if singleLabels[lb] {
		return base, lb, true, true
	}
	return base, lb, false, true
}

// parseFc2VideoID 解析 FC2 番号，返回 (base_code, label, is_single, ok)
// - 无 label 视为单部；有 label（-cd1 / -1 / -a 等）视为分集
func parseFc2VideoID(text string) (baseCode, label string, isSingle bool, ok bool) {
	loc := fc2Re.FindStringSubmatchIndex(text)
	if loc == nil {
		return "", "", false, false
	}
	if loc[1] < len(text) && isCharCode(text[loc[1]]) {
		return "", "", false, false
	}
	num := text[loc[2]:loc[3]]
	base := "FC2-PPV-" + num
	var lb string
	if loc[4] >= 0 {
		lb = text[loc[4]:loc[5]]
	}
	if lb == "" {
		return base, "", true, true
	}
	return base, lb, false, true
}

// parseVideoID 统一入口；mode 为 AV 或 FC2
func parseVideoID(text, mode string) (baseCode, label string, isSingle bool, ok bool) {
	switch strings.ToUpper(mode) {
	case "AV":
		return parseAvVideoID(text)
	case "FC2":
		return parseFc2VideoID(text)
	}
	return "", "", false, false
}

// ============ 文件工具 ============

// splitExt 拆分文件名与扩展名（扩展名含点，如 ".mp4"）
func splitExt(filename string) (stem, ext string) {
	i := strings.LastIndex(filename, ".")
	if i <= 0 {
		return filename, ""
	}
	return filename[:i], filename[i:]
}

// ============ 规则配置 ============

var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true,
	".flv": true, ".webm": true, ".m4v": true, ".ts": true, ".mpg": true,
}

// isVideoFile 是否为视频文件
func isVideoFile(name string) bool {
	_, ext := splitExt(name)
	return ext != "" && videoExts[strings.ToLower(ext)]
}
