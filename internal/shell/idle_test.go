package shell

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

type idleTestTimer struct {
	at      time.Time
	fn      func()
	stopped bool
}

type idleTestClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*idleTestTimer
}

func newIdleTestClock() *idleTestClock {
	return &idleTestClock{now: time.Unix(1000, 0)}
}

func (c *idleTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *idleTestClock) After(d time.Duration, fn func()) func() {
	c.mu.Lock()
	timer := &idleTestTimer{at: c.now.Add(d), fn: fn}
	c.timers = append(c.timers, timer)
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		timer.stopped = true
		c.mu.Unlock()
	}
}

func (c *idleTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var fn func()
		for _, timer := range c.timers {
			if !timer.stopped && !timer.at.After(c.now) {
				timer.stopped = true
				fn = timer.fn
				break
			}
		}
		c.mu.Unlock()
		if fn == nil {
			return
		}
		fn()
	}
}

func installIdleTestClock(s *Session) *idleTestClock {
	clock := newIdleTestClock()
	s.mu.Lock()
	s.idleNow = clock.Now
	s.idleAfter = clock.After
	s.mu.Unlock()
	return clock
}

func TestIdleReturnStartsAfterCommandCompletion(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	clock := installIdleTestClock(f.s)
	done := make(chan error, 1)
	go func() {
		_, _, err := f.run(t, "sleep 0.3; printf done")
		done <- err
	}()
	waitText(t, f, "[CLI] sleep")
	clock.Advance(5 * time.Minute)
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("命令执行期间不应自动交还: %v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	clock.Advance(59 * time.Second)
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("命令结束后不足一分钟不应交还: %v", got)
	}
	clock.Advance(time.Second)
	if got := f.s.ControlOwner(); got != ControlHuman {
		t.Fatalf("空闲一分钟后应交还人类: %v", got)
	}
}

func TestIdleReturnResetsForNewCommandsAndTakeover(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	clock := installIdleTestClock(f.s)
	if _, _, err := f.run(t, "printf first"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(59 * time.Second)
	if _, _, err := f.run(t, "printf second"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(59 * time.Second)
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("新命令结束后应重新计时: %v", got)
	}
	f.s.TakeOver()
	clock.Advance(5 * time.Minute)
	if got := f.s.ControlOwner(); got != ControlHumanTakeover {
		t.Fatalf("手动接管不应被计时器解除: %v", got)
	}
	if err := f.s.ReturnControl(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(59 * time.Second)
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("交还后应从头计时: %v", got)
	}
	clock.Advance(time.Second)
	if got := f.s.ControlOwner(); got != ControlHuman {
		t.Fatalf("交还后空闲一分钟应自动让出: %v", got)
	}
	if _, _, err := f.run(t, "printf reacquired"); err != nil || f.s.ControlOwner() != ControlAgent {
		t.Fatalf("自动交还后 CLI 无法重新接管: %v", err)
	}
}

func TestIdleReturnIgnoresBackgroundService(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	clock := installIdleTestClock(f.s)
	if _, _, err := f.run(t, "sleep 0.5 >/dev/null 2>&1 &"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if got := f.s.ControlOwner(); got != ControlHuman {
		t.Fatalf("前台命令结束后不应等待后台进程: %v", got)
	}
}

func TestControlStatusDoesNotResetIdle(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	clock := installIdleTestClock(f.s)
	if _, _, err := f.run(t, "printf done"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(59 * time.Second)
	if got := f.s.ControlStatus(); got != "agent" {
		t.Fatalf("查询控制状态错误: %q", got)
	}
	clock.Advance(time.Second)
	if got := f.s.ControlStatus(); got != "human" {
		t.Fatalf("状态查询不应重置空闲计时: %q", got)
	}
}

func TestStaleIdleTimerDoesNotReleaseDuringNewCommand(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	clock := installIdleTestClock(f.s)
	if _, _, err := f.run(t, "printf first"); err != nil {
		t.Fatal(err)
	}
	clock.mu.Lock()
	if len(clock.timers) == 0 {
		clock.mu.Unlock()
		t.Fatal("命令结束后未安排空闲计时")
	}
	stale := clock.timers[len(clock.timers)-1].fn
	clock.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, _, err := f.run(t, "sleep 0.3; printf second")
		done <- err
	}()
	waitText(t, f, "[CLI] sleep")
	stale()
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("过期计时器不应在新命令期间交还: %v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if got := f.s.ControlOwner(); got != ControlHuman {
		t.Fatalf("新命令结束后应从头计时: %v", got)
	}
}

func TestReleaseControlDoesNotOverrideTakeover(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	if _, _, err := f.run(t, "printf ready"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReleaseControl(); err != nil || f.s.ControlOwner() != ControlHuman {
		t.Fatalf("Agent 未能主动交还: %v", err)
	}
	if _, _, err := f.run(t, "printf again"); err != nil {
		t.Fatal(err)
	}
	f.s.TakeOver()
	if err := f.s.ReleaseControl(); err == nil || f.s.ControlOwner() != ControlHumanTakeover {
		t.Fatalf("Agent 不得解除人类手动接管: %v", err)
	}
	if result := f.s.Execute(context.Background(), "printf blocked"); result.Err == nil {
		t.Fatal("人类接管期间仍接受了 CLI 命令")
	}
}

func TestRemoteUnknownWaitsForCompletionBeforeIdleReturn(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	clock := installIdleTestClock(f.s)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := f.s.Execute(ctx, "sleep 0.5")
	if result.Err == nil {
		t.Fatal("远端命令应超时")
	}
	clock.Advance(5 * time.Minute)
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("远端结果未知时不应自动交还: %v", got)
	}
	waitRemoteReady(t, f)
	clock.Advance(59 * time.Second)
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("确认结束后不足一分钟不应交还: %v", got)
	}
	clock.Advance(time.Second)
	if got := f.s.ControlOwner(); got != ControlHuman {
		t.Fatalf("确认结束并空闲一分钟后应交还: %v", got)
	}
}

func TestRemoteUnknownSurvivesDisconnectAndLocalCommand(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	clock := installIdleTestClock(f.s)
	output, code, err := f.run(t, `printf '%s' "$$"`)
	if err != nil || code != 0 {
		t.Fatalf("读取隔离远端 Shell 进程失败: %q %d %v", output, code, err)
	}
	pid, err := strconv.Atoi(output)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if result := f.s.Execute(ctx, "sleep 1"); result.Err == nil {
		t.Fatal("远端命令应超时")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	f.waitReady(t)
	if _, _, err := f.run(t, "printf local"); err != nil {
		t.Fatalf("远端断开后本地命令失败: %v", err)
	}
	clock.Advance(2 * time.Minute)
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("远端结果仍未知时不应自动交还: %v", got)
	}
}
