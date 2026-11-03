//go:build e2e

package control

import "os"

// testDirectory 只供隔离自动化实例使用，不改变正常构建的单实例入口。
func testDirectory() string { return os.Getenv("WHOS_TYPING_TEST_DIR") }
