// stubkatago stands in for KataGo in the engine tests. It prints its
// arguments to stderr, one per line, then answers a few GTP commands on
// stdin until stdin ends or "quit".
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func main() {
	for _, a := range os.Args[1:] {
		fmt.Fprintln(os.Stderr, "arg:", a)
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("KataGo v1.18.2")
		fmt.Println("Git revision: 0000000")
		return
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		switch strings.TrimSpace(in.Text()) {
		case "version":
			fmt.Print("= 1.18.2\n\n")
		case "quit":
			fmt.Print("=\n\n")
			return
		default:
			fmt.Print("? unknown command\n\n")
		}
	}
}
