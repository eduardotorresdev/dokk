package dokku

import "testing"

func TestParseImage(t *testing.T) {
	cases := map[string]ImageRef{
		"nginx:1.27-alpine":                   {Registry: "docker.io", Repo: "library/nginx", Tag: "1.27-alpine"},
		"headwindmdm/hmdm:0.1.7":              {Registry: "docker.io", Repo: "headwindmdm/hmdm", Tag: "0.1.7"},
		"ghcr.io/a/portile:1.0.1":             {Registry: "ghcr.io", Repo: "a/portile", Tag: "1.0.1"},
		"ghcr.io/a/pbs/front@sha256:" + sha64: {Registry: "ghcr.io", Repo: "a/pbs/front", Digest: "sha256:" + sha64},
	}
	for in, want := range cases {
		if got, ok := ParseImage(in); !ok || got != want {
			t.Errorf("%s = %+v, %v", in, got, ok)
		}
	}
	if _, ok := ParseImage("e4f8f6bc2b2fb662aa383c00254b4188163c5fb7"); ok {
		t.Error("sha de git virou imagem")
	}
	if got, _ := ParseImage("nginx:1"); got.Name() != "nginx" {
		t.Error(got.Name())
	}
}

const sha64 = "baa27dbfdc0c94cd5a90327c4b0ef0ed3851831b420acdd3ee7469ed3c819c2b"

func TestVersionLess(t *testing.T) {
	if !versionLess("1.9.0", "1.10.0") || versionLess("2.0", "1.99") {
		t.Error("ordem natural errada")
	}
}

func TestPickReleases(t *testing.T) {
	got := pickReleases([]string{"pr-32-abc", "1.15.4", "sha-x", "v2", "1.27-alpine", "latest"})
	if len(got) != 3 {
		t.Errorf("got %v", got)
	}
	if got := pickReleases([]string{"latest", "x.sig"}); len(got) != 1 {
		t.Errorf("fallback %v", got)
	}
}
