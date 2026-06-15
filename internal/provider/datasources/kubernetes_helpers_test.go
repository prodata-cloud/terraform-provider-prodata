package datasources

import "testing"

func TestCompareK8sVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.31.4", "v1.31.4", 0},
		{"v1.31.4", "v1.31.3", 1},
		{"v1.31.3", "v1.31.4", -1},
		{"v1.32.0", "v1.31.9", 1},
		{"v2.0.0", "v1.99.99", 1},
		{"1.31.4", "v1.31.4", 0},      // 'v' prefix optional
		{"v1.31", "v1.31.0", 0},       // missing patch == 0
		{"v1.31.4-rc1", "v1.31.4", 0}, // pre-release suffix ignored on the patch
		{"v1.31.5-rc1", "v1.31.4", 1},
		{"", "v1.0.0", -1}, // malformed sorts low
		{"garbage", "v0.0.1", -1},
	}
	for _, tc := range cases {
		if got := compareK8sVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("compareK8sVersion(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestParseK8sVersion(t *testing.T) {
	cases := []struct {
		in   string
		want [3]int
	}{
		{"v1.31.4", [3]int{1, 31, 4}},
		{"1.31.4", [3]int{1, 31, 4}},
		{"v1.31", [3]int{1, 31, 0}},
		{"v1", [3]int{1, 0, 0}},
		{"v1.31.4-rc1", [3]int{1, 31, 4}},
		{"", [3]int{0, 0, 0}},
		{"garbage", [3]int{0, 0, 0}},
	}
	for _, tc := range cases {
		if got := parseK8sVersion(tc.in); got != tc.want {
			t.Errorf("parseK8sVersion(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
