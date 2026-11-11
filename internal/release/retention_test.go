package release

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestCoreRetentionFollowsVerifiedActivationHistory(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, binDir)
	first := fixtureCorePayload(t, bin, "3.0.0-dev.5")
	second := fixtureCorePayload(t, bin, "3.0.0-dev.6")
	third := fixtureCorePayload(t, bin, "3.0.0-dev.7")
	_ = fixtureCorePayload(t, bin, "3.0.0-dev.4") // Unknown to the ledger.

	history := releaseHistory{Schema: historySchema, Active: first}
	if err := activate(bin, history); err != nil {
		t.Fatal(err)
	}
	if err := activate(bin, planActivation(bin, second)); err != nil {
		t.Fatal(err)
	}
	checkCoreRetention(t, root)
	if err := activate(bin, planActivation(bin, third)); err != nil {
		t.Fatal(err)
	}
	checkCoreRetention(t, root, first.Version)

	// Same-version install keeps the verified rollback target.
	if err := activate(bin, planActivation(bin, third)); err != nil {
		t.Fatal(err)
	}
	checkCoreRetention(t, root, first.Version)

	// Rollback keeps the release it switched from, not the numerically closest.
	if err := activate(bin, planActivation(bin, first)); err != nil {
		t.Fatal(err)
	}
	checkCoreRetention(t, root, second.Version)
}

func TestCoreRetentionRefusesStaleHistoryOrChangedPayload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, bin string, history releaseHistory)
	}{
		{
			"marker rewritten by a previous installer",
			func(t *testing.T, bin string, _ releaseHistory) {
				marker := filepath.Join(bin, activeRelease)
				now := time.Now().Add(-time.Hour)
				if err := os.Chtimes(marker, now, now); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"rollback payload changed",
			func(t *testing.T, bin string, history releaseHistory) {
				changeCorePayload(t, bin, history.Previous.Version)
			},
		},
		{
			"active payload changed",
			func(t *testing.T, bin string, history releaseHistory) {
				changeCorePayload(t, bin, history.Active.Version)
			},
		},
		{
			"candidate payload changed",
			func(t *testing.T, bin string, history releaseHistory) {
				changeCorePayload(t, bin, history.Superseded[0].Version)
			},
		},
		{
			"candidate overlaps predecessor",
			func(t *testing.T, bin string, history releaseHistory) {
				history.Superseded[0] = *history.Previous
				if err := saveHistory(bin, history); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, binDir)
			first := fixtureCorePayload(t, bin, "3.0.0-dev.5")
			second := fixtureCorePayload(t, bin, "3.0.0-dev.6")
			third := fixtureCorePayload(t, bin, "3.0.0-dev.7")
			history := releaseHistory{
				Schema: historySchema, Active: third, Previous: &second, Superseded: []verifiedRelease{first},
			}
			if err := activate(bin, history); err != nil {
				t.Fatal(err)
			}
			checkCoreRetention(t, root, first.Version)
			history, ok := currentHistory(bin)
			if !ok {
				t.Fatal("valid active release history was rejected")
			}
			tc.change(t, bin, history)
			checkCoreRetention(t, root)
		})
	}
}

func fixtureCorePayload(t *testing.T, bin, version string) verifiedRelease {
	t.Helper()
	path := filepath.Join(bin, releasesDir, version)
	bridge := filepath.Join(path, filepath.FromSlash(bridgeName))
	if err := os.MkdirAll(filepath.Dir(bridge), 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{filepath.Join(path, executableName), bridge} {
		if err := os.WriteFile(file, []byte(version), 0600); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := payloadDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	return verifiedRelease{Version: version, Digest: digest}
}

func changeCorePayload(t *testing.T, bin, version string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, releasesDir, version, "additional-data"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
}

func checkCoreRetention(t *testing.T, root string, versions ...string) {
	t.Helper()
	got, err := PlanRetention(root)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, version := range versions {
		want = append(want, filepath.Join(root, binDir, releasesDir, version))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("PlanRetention = %q, want %q", got, want)
	}
}

func TestCoreRetentionAfterRecoveringToPrevious(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, binDir)
	previous := fixtureCorePayload(t, bin, "3.0.0-dev.6")
	current := fixtureCorePayload(t, bin, "3.0.0-dev.7")
	history := releaseHistory{Schema: historySchema, Active: current, Previous: &previous}
	if err := activate(bin, history); err != nil {
		t.Fatal(err)
	}
	changeCorePayload(t, bin, current.Version)
	if err := activate(bin, planActivation(bin, previous)); err != nil {
		t.Fatal(err)
	}
	recovered, ok := currentHistory(bin)
	if !ok || recovered.Previous != nil {
		t.Fatalf("invalid fallback history after recovery: %+v", recovered)
	}
	checkCoreRetention(t, root)
}
