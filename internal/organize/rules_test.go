package organize

import "testing"

func TestNormalizeAvCode(t *testing.T) {
	cases := map[string]string{
		"stars00220": "STARS-220",
		"ibw00184":   "IBW-184",
		"hmn00035":   "HMN-035",
		"abc00001":   "ABC-001",
		"STARS-220":  "STARS-220",
		"mida590":    "MIDA-590",
	}
	for in, want := range cases {
		if got := normalizeAvCode(in); got != want {
			t.Errorf("normalizeAvCode(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestParseAvVideoID(t *testing.T) {
	cases := []struct {
		in       string
		base     string
		label    string
		isSingle bool
		ok       bool
	}{
		{"ABC-123", "ABC-123", "", true, true},
		{"abc123", "ABC-123", "", true, true},
		{"abc123-uc", "ABC-123", "uc", true, true},
		{"abc123-c", "ABC-123", "c", true, true},
		{"abc123-CD1", "ABC-123", "cd1", false, true},
		// 边界：_ 后跟数字不应匹配（避免把 _456 当分集号）
		{"abc123_456", "", "", false, false},
		// 多集不一定有 -label，但 - 后跟长串视为分集
		{"vrkm00919-cd9", "VRKM-919", "cd9", false, true},
	}
	for _, c := range cases {
		base, label, isSingle, ok := parseAvVideoID(c.in)
		if ok != c.ok || base != c.base || label != c.label || isSingle != c.isSingle {
			t.Errorf("parseAvVideoID(%q) = (%q,%q,%v,%v), 期望 (%q,%q,%v,%v)",
				c.in, base, label, isSingle, ok, c.base, c.label, c.isSingle, c.ok)
		}
	}
}

func TestParseFc2VideoID(t *testing.T) {
	cases := []struct {
		in       string
		base     string
		label    string
		isSingle bool
		ok       bool
	}{
		{"FC2-PPV-1234567", "FC2-PPV-1234567", "", true, true},
		{"FC2PPV1234567", "FC2-PPV-1234567", "", true, true},
		{"fc2 ppv 1234567", "FC2-PPV-1234567", "", true, true},
		{"FC2-PPV-1234567-CD1", "FC2-PPV-1234567", "CD1", false, true},
	}
	for _, c := range cases {
		base, label, isSingle, ok := parseFc2VideoID(c.in)
		if ok != c.ok || base != c.base || label != c.label || isSingle != c.isSingle {
			t.Errorf("parseFc2VideoID(%q) = (%q,%q,%v,%v), 期望 (%q,%q,%v,%v)",
				c.in, base, label, isSingle, ok, c.base, c.label, c.isSingle, c.ok)
		}
	}
}

func TestSplitExt(t *testing.T) {
	cases := map[string][2]string{
		"a.mp4":   {"a", ".mp4"},
		"b.mkv":   {"b", ".mkv"},
		"noext":   {"noext", ""},
		".hidden": {".hidden", ""},
		"x.1.mp4": {"x.1", ".mp4"},
	}
	for in, want := range cases {
		stem, ext := splitExt(in)
		if stem != want[0] || ext != want[1] {
			t.Errorf("splitExt(%q) = (%q,%q), 期望 (%q,%q)", in, stem, ext, want[0], want[1])
		}
	}
}
