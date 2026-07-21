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
	"io"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	location "github.com/mostafakhairy0305-dot/golocation"
	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/singleton"
)

const (
	// streamBuffer is deep enough that a slow line of output cannot cost this
	// example a fix, and shallow enough to stay a demonstration.
	streamBuffer = 8
	// reportEvery is how often the running total is printed.
	reportEvery = 30 * time.Second
)

// main does nothing but report a failure, so that every cleanup run schedules
// in run stays on a path that actually reaches it. Calling log.Fatal from
// inside run would skip its own deferred stop and Close.
func main() {
	err := run()
	if err != nil {
		log.Fatalf("stream: %v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	out := os.Stdout

	emitf(out, "pid %d\n", os.Getpid())

	loc, err := location.Open(ctx, location.Config{Accuracy: location.AccuracyHigh})
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = loc.Close() }()

	emitf(out, "capabilities: %+v\n", loc.Capabilities())

	sub, err := loc.Subscribe(
		ctx,
		location.SubscriptionConfig{Buffer: streamBuffer, ReplayLatest: true},
	)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	report := time.NewTicker(reportEvery)
	defer report.Stop()

	stream(ctx, out, loc, sub, report)

	return nil
}

// emitf writes one line of output. Nothing useful can be done when stdout
// fails, so the error is dropped on purpose — once, here, rather than at every
// call site.
func emitf(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}

// stream is the event loop, split out so run stays a linear sequence of setup
// steps. Each of the subscription's three channels is drained by its own
// goroutine, which is what lets every drain be a plain range loop; stream
// itself only waits for the first of them to end, or for the context to. It
// ends when the context is cancelled or the subscription closes; neither is a
// failure, which is why it reports nothing.
func stream(
	ctx context.Context,
	out io.Writer,
	loc location.Locator,
	sub location.Subscription,
	report *time.Ticker,
) {
	var (
		fixes  atomic.Int64
		closed = make(chan struct{})
	)

	// Any one channel closing means the subscription is over, and all three
	// close together — so the first to notice wins and the rest are no-ops. The
	// guard closes closed exactly once, however many drains reach for it.
	closeOnce := singleton.MustNew(
		func(context.Context) (struct{}, error) {
			close(closed)

			return struct{}{}, nil
		},
		singleton.WithMaxAttempts(1),
	)
	// The guard's teardown only closes a channel, so it must run even once ctx
	// is cancelled: a context derived from ctx but stripped of the cancellation
	// keeps that promise while staying inherited.
	done := func() { _, _ = closeOnce.Get(context.WithoutCancel(ctx)) }

	go drainFixes(out, sub.Locations, &fixes, done)
	go drainStatuses(out, sub.Statuses, done)
	go drainErrors(out, sub.Errors, done)
	go reportPeriodically(ctx, out, loc, report, &fixes, closed)

	select {
	case <-ctx.Done():
	case <-closed:
	}

	emitf(out, "\nstopping after %d fixes\n", fixes.Load())
}

func drainFixes(out io.Writer, fixes <-chan geo.Fix, count *atomic.Int64, done func()) {
	defer done()

	for fix := range fixes {
		count.Add(1)

		emitf(out, "fix %.6f,%.6f ±%.0fm age=%s\n",
			fix.Latitude, fix.Longitude, fix.AccuracyMeters,
			fix.Age(time.Now()).Truncate(time.Millisecond))
	}
}

func drainStatuses(out io.Writer, statuses <-chan geo.Status, done func()) {
	defer done()

	for status := range statuses {
		emitf(out, "status: state=%d permission=%d %s\n",
			status.State, status.Permission, status.Message)
	}
}

func drainErrors(out io.Writer, errs <-chan error, done func()) {
	defer done()

	for err := range errs {
		emitf(out, "error: %v\n", err)
	}
}

func reportPeriodically(
	ctx context.Context,
	out io.Writer,
	loc location.Locator,
	report *time.Ticker,
	fixes *atomic.Int64,
	closed <-chan struct{},
) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-closed:
			return
		case <-report.C:
			emitf(out, "-- %d fixes so far, state=%d\n", fixes.Load(), loc.Status().State)
		}
	}
}
