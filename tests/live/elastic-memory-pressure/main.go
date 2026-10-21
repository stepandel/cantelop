// A guest-only load used by check-elastic-memory.py. Keep allocations reachable.
package main

import (
	"runtime"
	"time"
)

func main() {
	chunks := make([][]byte, 0, 16)
	for i := 0; i < 16; i++ {
		chunk := make([]byte, 48*1024*1024)
		for offset := 0; offset < len(chunk); offset += 4096 {
			chunk[offset] = 1
		}
		chunks = append(chunks, chunk)
		time.Sleep(5 * time.Second)
	}
	for {
		time.Sleep(30 * time.Second)
		runtime.KeepAlive(chunks)
	}
}
