package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBashStartupOrder(t *testing.T) {
	tests := []struct {
		name            string
		startup         string
		load, following int
	}{
		{
			name:    "installer marker and CRLF comments",
			startup: "source theme.sh\r\nsource /home/User\\ Name/.bmcshell-install/load.sh # BMCShell-Install: /home/User\\ Name\r\n\r\n# End\r\n",
			load:    2,
		},
		{
			name:      "command after installer",
			startup:   "source /local/.bmcshell-install/load.sh # BMCShell-Install: /local\n\n# Other theme\nsource other-theme.sh\n",
			load:      1,
			following: 4,
		},
		{
			name:    "conditional source at the end",
			startup: "if [[ $- == *i* ]]; then\n  builtin source -- \"$HOME/BMC Shell/bin/bmcshell.sh\" || return\nfi # End BMCShell\n",
			load:    2,
		},
		{
			name:      "command on the same line",
			startup:   ". '/home/User Name/BMC Shell/bin/bmcshell.sh'; eval \"$(theme init bash)\"",
			load:      1,
			following: 1,
		},
		{
			name:    "comment and lookalike",
			startup: "# source /local/.bmcshell-install/load.sh\nsource /local/bin/bmcshell.sh.disabled\n",
		},
		{
			name:    "BOM and no final newline",
			startup: "\uFEFFsource /local/bin/bmcshell.sh",
			load:    1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			load, following, err := bashStartupOrder(strings.NewReader(tt.startup))
			if err != nil || load != tt.load || following != tt.following {
				t.Fatalf("load = %d, following = %d, err = %v", load, following, err)
			}
		})
	}
}

func TestDoctorReportsBashStartupOrder(t *testing.T) {
	t.Setenv("BMCSHELL_HOME", t.TempDir())
	startup := filepath.Join(t.TempDir(), ".bashrc")
	t.Setenv("BMCSHELL_BRIDGE_BASHRC", startup)
	const load = "source /local/.bmcshell-install/load.sh # BMCShell-Install: /local\n"
	if err := os.WriteFile(startup, []byte(load+"export example=secret-marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := libraryCLI(t, 0, "", "doctor")
	if !strings.Contains(out, startup+":1") || !strings.Contains(out, "第 2 行") || strings.Contains(out, "secret-marker") {
		t.Fatalf("unexpected startup diagnostic: %q", out)
	}
	if out := libraryCLI(t, 0, "", "doctor", "-f", "missing"); out != "" {
		t.Fatalf("startup diagnostic leaked into missing dependencies: %q", out)
	}
	if err := os.WriteFile(startup, []byte("source theme.sh\n"+load+"# End\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out = libraryCLI(t, 0, "", "doctor", "-f", "ok")
	if !strings.Contains(out, "Bash 启动顺序：正常") || !strings.Contains(out, startup+":2") {
		t.Fatalf("correct startup order not recognized: %q", out)
	}
}
