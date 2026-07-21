// Command stream opens the native locator and prints fixes as they arrive.
//
// It also prints its own PID so memory behaviour can be sampled from outside:
//
//	go run ./examples/stream
//	while true; do ps -o rss= -p <pid>; sleep 10; done
//
// On macOS an unsigned, non-bundled binary usually never receives the
// CoreLocation authorization prompt, so this may sit in StateStarting with
// PermissionPromptRequired. That is an environment limitation, not a fault in
// the library — see the package documentation.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	location "github.com/mostafakhairy0305-dot/golocation"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("pid %d\n", os.Getpid())

	loc, err := location.Open(ctx, location.Config{Accuracy: location.AccuracyHigh})
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer loc.Close()

	fmt.Printf("capabilities: %+v\n", loc.Capabilities())

	sub, err := loc.Subscribe(ctx, location.SubscriptionConfig{Buffer: 8, ReplayLatest: true})
	if err != nil {
		log.Fatalf("subscribe: %v", err)
	}

	report := time.NewTicker(30 * time.Second)
	defer report.Stop()

	var fixes int
	for {
		select {
		case <-ctx.Done():
			fmt.Printf("\nstopping after %d fixes\n", fixes)
			return

		case fix, ok := <-sub.Locations:
			if !ok {
				return
			}
			fixes++
			fmt.Printf("fix %.6f,%.6f ±%.0fm age=%s\n",
				fix.Latitude, fix.Longitude, fix.AccuracyMeters,
				fix.Age(time.Now()).Truncate(time.Millisecond))

		case status, ok := <-sub.Statuses:
			if !ok {
				return
			}
			fmt.Printf("status: state=%d permission=%d %s\n",
				status.State, status.Permission, status.Message)

		case err, ok := <-sub.Errors:
			if !ok {
				return
			}
			fmt.Printf("error: %v\n", err)

		case <-report.C:
			fmt.Printf("-- %d fixes so far, state=%d\n", fixes, loc.Status().State)
		}
	}
}
