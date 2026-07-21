package core

import (
	"context"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	fanout "github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/port"
)

func benchService(tb testing.TB) *Service {
	tb.Helper()

	service := New(Options{
		MaximumAge:           time.Minute,
		DefaultChannelBuffer: 1,
		DefaultDropPolicy:    fanout.DropOldest,
	}, Features{})
	service.Attach(&fakeProvider{platform: "bench"})
	tb.Cleanup(func() { _ = service.Close() })

	return service
}

// The cost Next pays to register and unregister a waiter, with the wait itself
// excluded — a fix is already queued by the time the select runs. This is what
// the one-shot replaced a three-channel subscription plus a goroutine with.
func BenchmarkNextRoundTrip(b *testing.B) {
	service := benchService(b)
	fix := geo.Fix{Latitude: 51.5, Longitude: -0.12, AccuracyMeters: 10}
	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		id, events, err := service.hub.AddOnce()
		if err != nil {
			b.Fatalf("AddOnce: %v", err)
		}

		service.hub.BroadcastFix(fix)

		select {
		case <-events:
		case <-ctx.Done():
		}

		service.hub.RemoveOnce(id)
	}
}

// The same round trip through a full subscription, which is what Next used to
// open for every call.
func BenchmarkSubscriptionRoundTrip(b *testing.B) {
	service := benchService(b)
	fix := geo.Fix{Latitude: 51.5, Longitude: -0.12, AccuracyMeters: 10}

	b.ReportAllocs()

	for b.Loop() {
		ctx, cancel := context.WithCancel(context.Background())

		sub, err := service.Subscribe(
			ctx,
			fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropOldest},
		)
		if err != nil {
			b.Fatalf("Subscribe: %v", err)
		}

		service.hub.BroadcastFix(fix)
		<-sub.Locations
		cancel()
	}
}

// The steady state: a valid fix admitted, cached, and fanned out to a
// subscriber, with readiness already announced.
func BenchmarkPublishFix(b *testing.B) {
	service := benchService(b)

	ctx := b.Context()

	_, err := service.Subscribe(ctx, fanout.SubscriptionConfig{Buffer: 1})
	if err != nil {
		b.Fatalf("Subscribe: %v", err)
	}

	now := time.Now().UTC()
	fix := geo.Fix{
		Timestamp:      now,
		ReceivedAt:     now,
		Latitude:       51.5,
		Longitude:      -0.12,
		AccuracyMeters: 10,
	}
	service.PublishFix(fix) // settle the status transition out of the measured loop

	b.ReportAllocs()

	for b.Loop() {
		service.PublishFix(fix)
	}
}

func BenchmarkStatus(b *testing.B) {
	service := benchService(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		var status geo.Status
		for pb.Next() {
			status = service.Status()
		}

		if status.State == geo.StateClosed {
			b.Fatal("unreachable, and only here so the loop is not optimized away")
		}
	})
}
