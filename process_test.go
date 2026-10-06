package cask

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

const processData = "complete process object"

// TestCaskChild is only entered in explicitly spawned test processes. Pipes
// provide deterministic barriers at the actual native commit point.
func TestCaskChild(t *testing.T) {
	if os.Getenv("CASK_TEST_CHILD") != "1" {
		return
	}
	s, err := New(os.Getenv("CASK_TEST_ROOT"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	mode := os.Getenv("CASK_TEST_MODE")
	s.publish = func(dir *os.File, temp, final string) (publication, error) {
		fmt.Println("READY")
		var release [1]byte
		if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
			return publication{}, err
		}
		publisher := publishObject
		if mode == "link" || mode == "link-race" {
			publisher = testLinkPublication
		}
		result, err := publisher(dir, temp, final)
		if err != nil {
			return result, err
		}
		fmt.Println("PUBLISHED")
		if mode == "after" || mode == "link" {
			if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
				return result, err
			}
		}
		return result, nil
	}
	d := digestOf(t, SHA256, []byte(processData))
	if _, err := s.Put(testContext, d, strings.NewReader(processData)); err != nil {
		t.Fatal(err)
	}
}

type childProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  <-chan string
	ctx    context.Context
	cancel context.CancelFunc
}

func startChild(t *testing.T, root, mode string) *childProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(testContext, 30*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCaskChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CASK_TEST_CHILD=1", "CASK_TEST_ROOT="+root, "CASK_TEST_MODE="+mode)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	child := &childProcess{cmd: cmd, stdin: stdin, lines: lines, ctx: ctx, cancel: cancel}
	t.Cleanup(func() { cancel(); _ = stdin.Close() })
	return child
}

func (p *childProcess) await(t *testing.T, want string) {
	t.Helper()
	select {
	case line, ok := <-p.lines:
		if !ok || line != want {
			t.Fatalf("child expected %s, got %q (pipe open: %v)", want, line, ok)
		}
	case <-p.ctx.Done():
		t.Fatal("child barrier timeout", p.ctx.Err())
	}
}

func (p *childProcess) release(t *testing.T) {
	t.Helper()
	if _, err := p.stdin.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessCompetition(t *testing.T) {
	modes := []string{"race"}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		modes = append(modes, "link-race")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			s, root := newTestStore(t, Options{})
			a := startChild(t, root, "race")
			b := startChild(t, root, mode)
			a.await(t, "READY")
			b.await(t, "READY")
			a.release(t)
			b.release(t)
			a.await(t, "PUBLISHED")
			b.await(t, "PUBLISHED")
			for _, child := range []*childProcess{a, b} {
				if err := child.cmd.Wait(); err != nil {
					t.Fatal(err)
				}
			}
			d := digestOf(t, SHA256, []byte(processData))
			if _, err := s.Verify(testContext, d); err != nil {
				t.Fatal(err)
			}
			assertNoTemporary(t, root)
		})
	}
}

func TestCrashVisibility(t *testing.T) {
	for _, mode := range []string{"before", "after", "link"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "link" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				t.Skip("hard-link fallback is Unix only")
			}
			s, root := newTestStore(t, Options{})
			child := startChild(t, root, mode)
			child.await(t, "READY")
			d := digestOf(t, SHA256, []byte(processData))
			// Staging is complete and synced, but final is still absent.
			assertAbsent(t, objectPath(root, d))
			if _, _, err := s.Open(testContext, d); !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if mode != "before" {
				child.release(t)
				child.await(t, "PUBLISHED")
				r, _, err := s.Open(testContext, d)
				if err != nil {
					t.Fatal(err)
				}
				got, readErr := io.ReadAll(r)
				if err := errors.Join(readErr, r.Close()); err != nil || string(got) != processData {
					t.Fatalf("partial final object: %q %v", got, err)
				}
			}
			if err := child.cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := child.cmd.Wait(); err == nil {
				t.Fatal("child unexpectedly exited successfully")
			}
			if mode == "before" {
				assertAbsent(t, objectPath(root, d))
			} else if _, err := s.Verify(testContext, d); err != nil {
				t.Fatal(err)
			}
			if mode == "after" {
				assertNoTemporary(t, root)
			}
		})
	}
}
