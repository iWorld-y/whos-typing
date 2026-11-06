// Package sshlink 管理独立于 GUI 的 OpenSSH 复用连接，不保存密钥或重放命令。
package sshlink

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
)

var validTarget = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@:-]*$`)

type Link struct {
	Target, Socket string
	SSH            string
	Config         string
}

// New 使用独立的私有 socket 目录，不接管用户的其他 ControlMaster。
func New(target, config string) (*Link, error) {
	if !validTarget.MatchString(target) {
		return nil, errors.New("请使用 SSH 别名或 user@host，不接受选项、空格和 Shell 表达式")
	}
	dir := filepath.Join("/tmp", "whos-typing-ssh-"+strconv.Itoa(os.Geteuid()))
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("SSH socket 目录必须由当前用户拥有且权限为 0700")
	}
	identity := target
	if config != "" {
		resolved, err := filepath.Abs(config)
		if err != nil {
			return nil, err
		}
		config = resolved
		data, err := os.ReadFile(config)
		if err != nil {
			return nil, fmt.Errorf("读取 SSH 配置失败: %w", err)
		}
		identity += "\x00" + config + "\x00" + string(data)
	}
	sum := sha256.Sum256([]byte(identity))
	return &Link{Target: target, Socket: filepath.Join(dir, hex.EncodeToString(sum[:16])), SSH: "/usr/bin/ssh", Config: config}, nil
}

func (l *Link) config() []string {
	if l.Config != "" {
		return []string{"-F", l.Config}
	}
	return nil
}
func (l *Link) base() []string {
	return append(l.config(), []string{"-S", l.Socket, "-o", "ControlMaster=no", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10"}...)
}
func (l *Link) check() error {
	args := append(l.base(), "-O", "check", l.Target)
	return exec.Command(l.SSH, args...).Run()
}
func (l *Link) Connected() bool { return l.check() == nil }

// Connect 只有显式调用才允许建立连接；认证/主机密钥仍由 OpenSSH 配置管理。
func (l *Link) Connect() error {
	// 序列化建立/关闭，避免并发启动时生成无人管理的 master。
	lock, err := os.OpenFile(l.Socket+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if l.Connected() {
		return nil
	}
	args := append(l.config(), []string{"-M", "-N", "-f", "-S", l.Socket, "-o", "ControlPersist=600", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", l.Target}...)
	// master 在后台继续运行，不能继承 CombinedOutput 管道，否则调用方可能等到 master 退出。
	log, err := os.CreateTemp(filepath.Dir(l.Socket), "connect-")
	if err != nil {
		return err
	}
	defer os.Remove(log.Name())
	defer log.Close()
	cmd := exec.Command(l.SSH, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Run(); err != nil {
		_, _ = log.Seek(0, 0)
		output, _ := io.ReadAll(io.LimitReader(log, 8192))
		return fmt.Errorf("建立 SSH 连接失败: %w: %s", err, output)
	}
	if !l.Connected() {
		return errors.New("未能验证复用连接；不会执行远端命令")
	}
	return nil
}

// Run 通过已建立的 socket 执行。ProxyCommand=false 阻止 socket 消失后的直连回退。
// 命令交给远端 Shell，本机始终使用 exec 参数数组，不解析用户命令。
func (l *Link) Run(command string, out, errOut io.Writer) (int, error) {
	if command == "" {
		return 2, errors.New("命令不能为空")
	}
	if !l.Connected() {
		return 1, errors.New("连接不可用，请显式 connect；命令未发送")
	}
	args := append(l.base(), "-o", "ProxyCommand=false", "-o", "ProxyJump=none", "-T", l.Target, command)
	cmd := exec.Command(l.SSH, args...)
	cmd.Stdout = out
	cmd.Stderr = errOut
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.ExitCode() == 255 {
			return 255, errors.New("SSH 执行失败或远端返回 255，结果未知；不要自动重试")
		}
		return exit.ExitCode(), nil
	}
	return 1, err
}
func (l *Link) Disconnect() error {
	lock, err := os.OpenFile(l.Socket+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// 不删除 socket 文件：OpenSSH 自己清理，避免误删迟到的新连接。
	args := append(l.base(), "-O", "exit", l.Target)
	output, err := exec.Command(l.SSH, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("关闭连接失败: %w: %s", err, output)
	}
	return nil
}
