package dokku

import "testing"

func TestParseVersions(t *testing.T) {
	log := "\x00bbb\x1f2026-10-02T20:25:33+00:00\x1fDokku\x1fAutomated commit\n\x00aaa\x1f2026-10-01T10:00:00+00:00\x1fDokku\x1fInitial commit\n"
	images := "\x00bbb\x1f2026-10-02T20:25:33+00:00\x1fDokku\x1fAutomated commit\n\ndiff --git a/Dockerfile b/Dockerfile\n@@ -1 +1 @@\n-FROM img:1\n+FROM img:2\n" +
		"\x00aaa\x1f2026-10-01T10:00:00+00:00\x1fDokku\x1fInitial commit\n\n+FROM img:1\n"
	v := parseVersions(log, images, "aaa")
	if len(v) != 2 || v[0].Image != "img:2" || v[1].Image != "img:1" || v[0].Current || !v[1].Current {
		t.Fatalf("versions = %+v", v)
	}
	if v[0].Date.IsZero() {
		t.Error("data não lida")
	}
}
