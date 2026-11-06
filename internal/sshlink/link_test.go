package sshlink

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fake(t *testing.T, body string) *Link {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "ssh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return &Link{Target: "test-host", Socket: filepath.Join(dir, "socket"), SSH: p}
}
func TestRejectTargets(t *testing.T) {
	for _, s := range []string{"", "-oProxyCommand=bad", "host;touch /tmp/bad", "$(whoami)", "host\ncommand", "host name"} {
		if _, err := New(s, ""); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestDisconnectedNeverSendsCommand(t *testing.T) {
	l := fake(t, "exit 1\n")
	var out bytes.Buffer
	_, err := l.Run("echo forbidden", &out, &out)
	if err == nil || !strings.Contains(err.Error(), "命令未发送") {
		t.Fatal(err)
	}
}
func TestMuxLossCannotReconnect(t *testing.T) {
	l := fake(t, `case " $* " in *" -O check "*) exit 0;; esac
case " $* " in *" ProxyCommand=false "*) ;; *) echo missing-fallback-guard; exit 3;; esac
case " $* " in *" ProxyJump=none "*) ;; *) exit 4;; esac
exit 255
`)
	var out bytes.Buffer
	code, err := l.Run("echo no-replay", &out, &out)
	if code != 255 || err == nil || !strings.Contains(err.Error(), "未知") {
		t.Fatalf("%d %v %s", code, err, out.String())
	}
}
func TestExitStatusAndLiteralCommand(t *testing.T) {
	l := fake(t, `case " $* " in *" -O check "*) exit 0;; esac
for arg do last="$arg"; done
printf '%s' "$last"
exit 7
`)
	var out bytes.Buffer
	command := "printf '%s' '$HOME; `whoami`'"
	code, err := l.Run(command, &out, &out)
	if code != 7 || err != nil || out.String() != command {
		t.Fatalf("%d %v %q", code, err, out.String())
	}
}
func TestConnectDoesNotDuplicateLiveMaster(t *testing.T) {
	l := fake(t, `case " $* " in *" -O check "*) exit 0;; esac
exit 99
`)
	if err := l.Connect(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigSeparatesConnections(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	os.WriteFile(p, []byte("Host server\n HostName one\n"), 0600)
	a, err := New("server", p)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("Host server\n HostName two\n"), 0600)
	b, err := New("server", p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Socket == b.Socket {
		t.Fatal("配置变更复用了旧连接")
	}
}

// 使用真实 OpenSSH 验证 socket 丢失后失败；ProxyCommand=false 不进行网络连接。
func TestRealSSHMissingSocketFailsClosed(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ssh"); err != nil {
		t.Skip("requires OpenSSH")
	}
	l := &Link{Target: "localhost", Socket: filepath.Join(t.TempDir(), "missing"), SSH: "/usr/bin/ssh", Config: "/dev/null"}
	args := append(l.base(), "-o", "ProxyCommand=false", "-o", "ProxyJump=none", "-T", l.Target, "echo SHOULD_NOT_RUN")
	var output bytes.Buffer
	cmd := exec.Command(l.SSH, args...)
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err == nil || strings.Contains(output.String(), "SHOULD_NOT_RUN") {
		t.Fatalf("unexpected result: %v %s", err, output.String())
	}
}
