// 本文件验证 macOS 上关闭终端时前台命令的退出行为。
package shell

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCloseWithForegroundCommand(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("未安装 fish")
	}
	for _, tc := range []struct {
		command   string
		hangupSet bool
	}{
		{command: "sleep 30"},
		{command: `sh -c 'trap "" HUP; printf "HUP_READY\n"; exec sleep 30'`, hangupSet: true},
	} {
		t.Run(fmt.Sprintf("ignore-hangup=%v", tc.hangupSet), func(t *testing.T) {
			f := newFixture(t, fish)
			result := make(chan error, 1)
			go func() {
				_, _, err := f.run(t, tc.command)
				result <- err
			}()
			group := waitForegroundGroup(t, f.s)
			shellPID := f.s.command.Process.Pid
			if sid, err := syscall.Getsid(group); err != nil || sid != shellPID {
				t.Fatalf("测试进程不属于当前 Shell 会话: sid=%d error=%v", sid, err)
			}
			// 只回收此测试在独立 Shell 会话内创建的前台进程。
			t.Cleanup(func() {
				if sid, err := syscall.Getsid(group); err == nil && sid == shellPID {
					_ = syscall.Kill(-group, syscall.SIGKILL)
				}
			})
			if tc.hangupSet {
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) && !strings.Contains(f.screen(), "\nHUP_READY") {
					time.Sleep(10 * time.Millisecond)
				}
				if !strings.Contains(f.screen(), "\nHUP_READY") {
					t.Fatalf("测试命令未完成 HUP 配置: %q", f.screen())
				}
			}
			closed := make(chan error, 1)
			go func() { closed <- f.term.Close() }()
			timer := time.NewTimer(1500 * time.Millisecond)
			defer timer.Stop()
			select {
			case err := <-closed:
				if err != nil {
					t.Fatalf("关闭终端失败: %v", err)
				}
			case <-timer.C:
				t.Fatal("终端关闭未及时返回")
			}
			select {
			case <-f.readEnd:
			case <-timer.C:
				t.Fatal("终端读取未及时结束")
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("关闭终端时命令被报告为成功")
				}
			case <-timer.C:
				t.Fatal("关闭终端后命令未及时返回")
			}
			select {
			case <-f.s.exited:
			default:
				t.Fatal("终端结束后 Shell 仍在运行")
			}
			if tc.hangupSet {
				if err := syscall.Kill(-group, 0); err != nil {
					t.Fatalf("显式忽略 HUP 的测试进程应保持存活: %v", err)
				}
				return
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if err := syscall.Kill(-group, 0); err == syscall.ESRCH {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("普通前台命令在终端关闭后仍然存活")
		})
	}
}
