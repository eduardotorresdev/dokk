package dokku

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]string{"dokku version 0.38.31\n": "0.38.31", "v0.38.31": "0.38.31", "": ""} {
		if got := parseVersion(in); got != want {
			t.Errorf("parseVersion(%q) = %q, esperado %q", in, got, want)
		}
	}
}

func TestHostInfo(t *testing.T) {
	ubuntu := func(v string) string { return "NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"" + v + "\"\n" }
	fedora := "ID=fedora\nVERSION_ID=41\n"
	cases := []struct {
		name, goos, arch, rel string
		euid                  int
		force                 Mode
		ok                    bool
		method                string
		reason, code, arg     string
	}{
		{"ubuntu 24.04 root", "linux", "amd64", ubuntu("24.04"), 0, ModeAuto, true, MethodBootstrap, "", "", ""},
		{"ubuntu 20.04 vai de docker", "linux", "amd64", ubuntu("20.04"), 0, ModeAuto, true, MethodDocker, "", "", ""},
		{"fedora vai de docker", "linux", "arm64", fedora, 0, ModeAuto, true, MethodDocker, "", "", ""},
		{"ubuntu forçado docker", "linux", "arm64", ubuntu("24.04"), 0, ModeDocker, true, MethodDocker, "", "", ""},
		{"fedora forçado host", "linux", "amd64", fedora, 0, ModeHost, true, MethodBootstrap, "", "", ""},
		{"debian 12 sem root", "linux", "arm64", "ID=debian\nVERSION_ID=\"12\"\n", 1000, ModeAuto, false, MethodBootstrap, "root", "root", ""},
		{"darwin", "darwin", "arm64", "", 0, ModeAuto, false, "", "darwin", "os", "darwin"},
		{"arch 386", "linux", "386", ubuntu("22.04"), 0, ModeAuto, false, MethodBootstrap, "Arquitetura", "arch", "386"},
	}
	for _, c := range cases {
		h := hostInfo(c.goos, c.arch, c.rel, c.euid, c.force)
		if h.CanInstall != c.ok || h.Method != c.method || !strings.Contains(h.Reason, c.reason) || h.ReasonCode != c.code || h.ReasonArg != c.arg {
			t.Errorf("%s: %+v", c.name, h)
		}
	}
	if h := hostInfo("linux", "amd64", "ID=rocky\nID_LIKE=\"rhel centos fedora\"\n", 0, ModeAuto); h.OSLike != "rhel centos fedora" {
		t.Errorf("ID_LIKE: %+v", h)
	}
}

func TestGlobalDomains(t *testing.T) {
	c := NewClient(fakeRunner{})
	c.HomeRoot = t.TempDir()
	if got := c.GlobalDomains(); got == nil || len(got) != 0 {
		t.Fatalf("sem VHOST esperava [], veio %#v", got)
	}
	must(t, os.WriteFile(filepath.Join(c.HomeRoot, "VHOST"), []byte("a.com\n\nb.com\n"), 0o644))
	if got := c.GlobalDomains(); !reflect.DeepEqual(got, []string{"a.com", "b.com"}) {
		t.Fatalf("domínios: %v", got)
	}
}

func TestValidDomainAndSSHKey(t *testing.T) {
	for d, ok := range map[string]bool{"a.com": true, "1.2.3.4.sslip.io": true, "x": false, "-a.com": false, "a..com": false, "A.com": false, "a.com\n": false} {
		if ValidDomain(d) != ok {
			t.Errorf("ValidDomain(%q) != %v", d, ok)
		}
	}
	for k, ok := range map[string]bool{
		"ssh-ed25519 AAAAC3Nza eduardo@pc":   true,
		"  ssh-rsa AAAAB3== \n":              true,
		"ecdsa-sha2-nistp256 AAAAE2":         true,
		"ssh-ed25519 AAAA\nssh-ed25519 BBBB": false,
		"-o ssh-ed25519 AAAA":                false,
		"ssh-ed25519":                        false,
		"ssh-foo AAAA":                       false,
	} {
		if ValidSSHKey(k) != ok {
			t.Errorf("ValidSSHKey(%q) != %v", k, ok)
		}
	}
}

func TestVersionAbsent(t *testing.T) {
	if _, err := NewClient(fakeRunner{}).Version(context.Background()); err == nil {
		t.Fatal("sem dokku esperava erro")
	}
	v, err := NewClient(fakeRunner{"dokku version": "dokku version 0.38.31\n"}).Version(context.Background())
	if err != nil || v != "0.38.31" {
		t.Fatalf("versão %q, %v", v, err)
	}
}
