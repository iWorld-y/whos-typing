// whos-typing 启动原生终端应用。
package main

import (
	"fmt"
	"os"

	"whos-typing/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
