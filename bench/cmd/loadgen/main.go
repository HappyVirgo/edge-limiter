// Command loadgen drives a running edge-limiter's POST /v1/check endpoint and
// prints throughput and latency percentiles.
//
//	go run ./bench/cmd/loadgen -url http://localhost:8080 -c 64 -d 10s
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/HappyVirgo/edge-limiter/bench/load"
)

func main() {
	var o load.Options
	flag.StringVar(&o.URL, "url", "http://localhost:8080", "edge-limiter base URL")
	flag.IntVar(&o.Concurrency, "c", 64, "concurrent workers")
	flag.DurationVar(&o.Duration, "d", 10*time.Second, "test duration")
	flag.IntVar(&o.Keys, "keys", 10000, "distinct keys")
	flag.Parse()
	fmt.Println(load.Run(context.Background(), o))
}
