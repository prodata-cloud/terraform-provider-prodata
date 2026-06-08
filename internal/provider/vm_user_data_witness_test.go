package provider

import "testing"

// TestSshEnabledGating locks the witness gate (design §5 fix 1): the master flag
// PRODATA_VM_TEST_SSH_REACHABLE=1 is always required; PUBLIC_IP_ID is required ONLY in
// public-IP mode and dropped when PRODATA_VM_TEST_SSH_VIA_PRIVATE=1. A missing required
// var must yield false (skip), never a silent true.
func TestSshEnabledGating(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"master off", map[string]string{"PRODATA_VM_TEST_SSH_REACHABLE": ""}, false},
		{"public mode fully set", map[string]string{
			"PRODATA_VM_TEST_SSH_REACHABLE": "1", "PRODATA_VM_TEST_SSH_KEY": "k",
			"PRODATA_VM_TEST_SSH_PRIVKEY": "/k", "PRODATA_VM_TEST_PUBLIC_IP_ID": "7",
		}, true},
		{"public mode missing public ip", map[string]string{
			"PRODATA_VM_TEST_SSH_REACHABLE": "1", "PRODATA_VM_TEST_SSH_KEY": "k",
			"PRODATA_VM_TEST_SSH_PRIVKEY": "/k", "PRODATA_VM_TEST_PUBLIC_IP_ID": "",
		}, false},
		{"public mode missing ssh key", map[string]string{
			"PRODATA_VM_TEST_SSH_REACHABLE": "1", "PRODATA_VM_TEST_SSH_KEY": "",
			"PRODATA_VM_TEST_SSH_PRIVKEY": "/k", "PRODATA_VM_TEST_PUBLIC_IP_ID": "7",
		}, false},
		{"private mode no public ip needed", map[string]string{
			"PRODATA_VM_TEST_SSH_REACHABLE": "1", "PRODATA_VM_TEST_SSH_VIA_PRIVATE": "1",
			"PRODATA_VM_TEST_SSH_KEY": "k", "PRODATA_VM_TEST_SSH_PRIVKEY": "/k",
			"PRODATA_VM_TEST_PUBLIC_IP_ID": "",
		}, true},
		{"private mode missing privkey", map[string]string{
			"PRODATA_VM_TEST_SSH_REACHABLE": "1", "PRODATA_VM_TEST_SSH_VIA_PRIVATE": "1",
			"PRODATA_VM_TEST_SSH_KEY": "k", "PRODATA_VM_TEST_SSH_PRIVKEY": "",
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range []string{
				"PRODATA_VM_TEST_SSH_REACHABLE", "PRODATA_VM_TEST_SSH_VIA_PRIVATE",
				"PRODATA_VM_TEST_SSH_KEY", "PRODATA_VM_TEST_SSH_PRIVKEY", "PRODATA_VM_TEST_PUBLIC_IP_ID",
			} {
				t.Setenv(k, c.env[k]) // unset keys default to ""
			}
			if got := sshEnabled(t); got != c.want {
				t.Fatalf("sshEnabled = %v, want %v", got, c.want)
			}
		})
	}
}
