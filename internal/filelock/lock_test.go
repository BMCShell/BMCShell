package filelock

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLockReleasesOnProcessExit(t *testing.T) {
	const pathVariable = "BMCSHELL_LOCK_TEST_PATH"
	if path := os.Getenv(pathVariable); path != "" {
		unlock, err := Acquire(path)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		fmt.Println("locked")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}

	path := filepath.Join(t.TempDir(), "test.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockReleasesOnProcessExit$")
	cmd.Env = append(os.Environ(), pathVariable+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("test process did not acquire lock: %q %v", line, err)
	}
	unlock, err := Acquire(path)
	if !errors.Is(err, ErrBusy) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("second process acquired a held lock: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(time.Second)
	for {
		unlock, err := Acquire(path)
		if err == nil {
			unlock()
			break
		}
		if !errors.Is(err, ErrBusy) || !time.Now().Before(deadline) {
			t.Fatalf("lock not released after termination: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
