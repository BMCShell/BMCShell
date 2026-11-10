package librarydb

import "testing"

func TestThreeWayLibraryMerge(t *testing.T) {
	layout := tableLayout{columns: []string{"key", "value"}, keyIndexes: []int{0}}
	before := map[string][]any{
		"delete": {"delete", "old"}, "local": {"local", "old"},
		"conflict": {"conflict", "old"}, "remote": {"remote", "old"},
		"same": {"same", "value"},
	}
	local := map[string][]any{
		"delete": before["delete"], "local": {"local", "local edit"},
		"conflict": {"conflict", "local edit"}, "remote": before["remote"],
		"same": before["same"],
	}
	remote := map[string][]any{
		"local": before["local"], "conflict": {"conflict", "remote edit"},
		"remote": {"remote", "remote edit"}, "same": before["same"], "new": {"new", "added"},
	}
	changes := compareRows("records", layout, local, before, remote, false)
	if len(changes) != 4 || !changes[0].Conflict || changes[0].UseRemote ||
		changes[1].Remote != nil || !changes[1].UseRemote ||
		changes[2].Local != nil || !changes[2].UseRemote ||
		changes[3].Key[0] != "remote" || !changes[3].UseRemote {
		t.Fatalf("unexpected merge plan: %#v", changes)
	}
	if mergeKey([]any{int64(1e18)}, layout) != mergeKey([]any{float64(1e18)}, layout) {
		t.Fatal("SQLite-equivalent numeric keys do not match")
	}
	if mergeKey([]any{"a"}, layout) == mergeKey([]any{[]byte("a")}, layout) {
		t.Fatal("SQLite TEXT and BLOB keys must be distinct")
	}
}
