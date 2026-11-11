package session

import (
	"testing"
)

func TestSNMPEnterprisesPublicContext(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{"", true}, {"0", true}, {"1.3.6.1.4.1.54357", true}, {"value", false}} {
		t.Setenv("BMCSHELL_CONTEXT", "{\"target.ip\":\"192.0.2.10\",\"snmp.enterprises\":\""+tc.value+"\"}")
		current, err := Current()
		if (err == nil) != tc.valid || tc.valid && current["snmp.enterprises"] != tc.value {
			t.Fatalf("context %q = %v, %v", tc.value, current, err)
		}
	}
}
