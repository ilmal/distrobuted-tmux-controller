package config

import "testing"

func TestHiddenHost(t *testing.T) {
	c := &Config{
		Hostname: "cn1",
		Hosts: map[string]HostCfg{
			"cn1":    {},
			"main":   {SSH: "main"},
			"laptop": {SSH: "laptop", Hidden: true},
			"other":  {SSH: "other"},
		},
	}
	cases := []struct {
		host         string
		selfDeclared bool
		want         bool
	}{
		{"main", false, false},
		{"laptop", false, true},   // local config says so
		{"other", true, true},     // its own heartbeat says so
		{"cn1", false, false},     // never hidden from itself
		{"cn1", true, false},      // ...not even if it self-declares
		{"unknown", true, true},   // a self-declared unknown host is still hidden
		{"unknown", false, false}, // ...but an unknown host with no flag is not
	}
	for _, tc := range cases {
		if got := c.HiddenHost(tc.host, tc.selfDeclared); got != tc.want {
			t.Errorf("HiddenHost(%q, %v) = %v, want %v", tc.host, tc.selfDeclared, got, tc.want)
		}
	}
}

func TestIsHiddenIgnoresHeartbeatFlag(t *testing.T) {
	c := &Config{
		Hostname: "cn1",
		Hosts:    map[string]HostCfg{"laptop": {Hidden: true}},
	}
	if !c.IsHidden("laptop") {
		t.Fatal("local hidden flag not honored")
	}
	if c.HiddenHost("laptop", false) != c.IsHidden("laptop") {
		t.Fatal("IsHidden should equal HiddenHost with a false heartbeat flag")
	}
}

func TestSelfHidden(t *testing.T) {
	c := &Config{Hostname: "laptop", Hosts: map[string]HostCfg{"laptop": {Hidden: true}}}
	if !c.SelfHidden() {
		t.Fatal("laptop should report itself hidden")
	}
	c2 := &Config{Hostname: "cn1", Hosts: map[string]HostCfg{"cn1": {}, "laptop": {Hidden: true}}}
	if c2.SelfHidden() {
		t.Fatal("cn1 should not report itself hidden")
	}
}

func TestSSHFor(t *testing.T) {
	c := &Config{Hosts: map[string]HostCfg{"main": {SSH: "main.example"}}}
	if got, ok := c.SSHFor("main"); !ok || got != "main.example" {
		t.Fatalf("SSHFor(main) = %q, %v", got, ok)
	}
	if _, ok := c.SSHFor("nope"); ok {
		t.Fatal("SSHFor should report ok=false for an unknown host")
	}
}
