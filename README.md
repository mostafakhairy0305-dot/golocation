# golocation

One cross-platform location API for Go, backed by the native provider on each operating
system: **CoreLocation** on macOS, **GeoClue2** on Linux, and **Windows.Devices.Geolocation**
on Windows.

No cgo, no daemon, no polling loop of your own. `Open` starts the provider and returns a
`Locator`; you read fixes as a cached value, as a one-shot wait, or as a stream.

MIT licensed · Go 1.25+

```go
loc, err := location.Open(ctx, location.DefaultConfig())
if err != nil {
    return err
}
defer loc.Close()

fix, err := loc.Current(ctx)
fmt.Printf("%.6f, %.6f ±%.0fm\n", fix.Latitude, fix.Longitude, fix.AccuracyMeters)
```

---

## Install

```sh
go get github.com/mostafakhairy0305-dot/golocation
```

```go
import location "github.com/mostafakhairy0305-dot/golocation"
```

Requires **Go 1.25 or newer**. The module builds with `CGO_ENABLED=0` on every supported
platform — macOS goes through [purego](https://github.com/ebitengine/purego), Linux
through a pure-Go D-Bus client, and Windows through WinRT syscall bindings.

### Platform requirements

| Platform | Needs |
|---|---|
| **macOS** | A signed application bundle whose `Info.plist` contains `NSLocationWhenInUseUsageDescription`. Without it the authorization prompt never appears — see [Permissions](#permissions). |
| **Linux** | GeoClue2 running and reachable on the D-Bus **system** bus (`geoclue-2.0` on most distributions). |
| **Windows** | amd64 or arm64. Location must be enabled in *Settings → Privacy & security → Location*. |

On any other OS or architecture, `Open` fails immediately with `ErrUnsupported` rather
than handing back a locator that can never produce a fix.

---

## Quick start

### A single fix

`Current` returns the cached fix when it is still fresh (younger than `Config.MaximumAge`),
and otherwise waits for the next one.

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

loc, err := location.Open(ctx, location.DefaultConfig())
if err != nil {
    return err
}
defer loc.Close()

fix, err := loc.Current(ctx)
if err != nil {
    return err
}
fmt.Printf("%.6f, %.6f ±%.0fm\n", fix.Latitude, fix.Longitude, fix.AccuracyMeters)
```

### Always a fresh fix

`Next` ignores the cache and waits for a fix admitted *after* the call starts. `Last`
is the opposite — it never blocks and never waits.

```go
fix, err := loc.Next(ctx)      // blocks until a new fix arrives
cached, ok := loc.Last()       // returns immediately; ok is false before the first fix
```

### A stream

`Subscribe` opens an independent stream. Every subscription has its own channels and its
own backpressure policy, so a slow consumer never blocks the provider or another
subscriber.

```go
sub, err := loc.Subscribe(ctx, location.SubscriptionConfig{
    Buffer:       8,
    ReplayLatest: true, // deliver the current fix immediately, if there is one
})
if err != nil {
    return err
}

for {
    select {
    case <-ctx.Done():
        return ctx.Err()

    case fix, ok := <-sub.Locations:
        if !ok {
            return nil // the locator closed
        }
        fmt.Printf("fix %.6f,%.6f age=%s\n",
            fix.Latitude, fix.Longitude, fix.Age(time.Now()).Truncate(time.Millisecond))

    case status, ok := <-sub.Statuses:
        if !ok {
            return nil
        }
        fmt.Printf("status: state=%d permission=%d %s\n",
            status.State, status.Permission, status.Message)

    case err, ok := <-sub.Errors:
        if !ok {
            return nil
        }
        fmt.Printf("error: %v\n", err)
    }
}
```

A complete runnable version is in [`examples/stream`](examples/stream/main.go):

```sh
go run ./examples/stream
```

---

## Platform support

| Provider | Build constraint | Altitude | Vertical accuracy | Speed | Heading | Source |
|---|---|:---:|:---:|:---:|:---:|:---:|
| CoreLocation | `darwin && (amd64 \| arm64)` | ✅ | ✅ | ✅ | ✅ | ❌ |
| GeoClue2 | `linux` (any arch) | ✅ | ❌ | ✅ | ✅ | ❌ |
| WinRT | `windows && (amd64 \| arm64)` | ✅ | ✅ | ✅ | ✅ | ✅ |
| *unsupported* | everything else | — | — | — | — | — |

`Latitude`, `Longitude`, `AccuracyMeters`, `Timestamp`, and `ReceivedAt` are always
populated on an accepted fix. Everything else is optional, in two senses:

- **`Capabilities()`** tells you what the *platform* can ever supply. On Linux
  `Capabilities().VerticalAccuracy` is `false`, so `VerticalAccuracyMeters` will never
  be meaningful there.
- **`fix.Has(field)`** tells you whether *this particular sample* carried the value. A
  platform that supports altitude may still emit a fix without one.

```go
if fix.Has(location.FieldAltitude) {
    fmt.Printf("altitude %.1fm\n", fix.AltitudeMeters)
}
```

---

## API reference

Everything below is exported from the root `location` package. The `geo` package holds
the domain values and is re-exported here, so importing `location` alone is enough.

### Entry point

| Symbol | Signature | Notes |
|---|---|---|
| `Open` | `func(ctx context.Context, config Config) (Locator, error)` | Starts the native provider. Blocks until it starts or `Config.StartTimeout` elapses. |
| `DefaultConfig` | `func() Config` | Production-oriented defaults; see the [Config](#config) table. |

### Locator

The single interface callers hold. Identical on all three platforms.

| Method | Blocks? | Behaviour |
|---|---|---|
| `Current(ctx) (Fix, error)` | Sometimes | Returns the cached fix if it is younger than `MaximumAge`; otherwise waits like `Next`. |
| `Next(ctx) (Fix, error)` | Yes | Waits for a fix admitted after the call starts. Never returns a cached value. |
| `Last() (Fix, bool)` | No | The newest admitted fix. `false` before the first one arrives. |
| `Subscribe(ctx, SubscriptionConfig) (Subscription, error)` | No | Opens an independent stream. Ends when `ctx` is done or the locator closes. |
| `Status() Status` | No | Current service and permission state. |
| `Capabilities() Capabilities` | No | Which optional `Fix` fields the active provider can supply. |
| `Close() error` | No | Stops the provider and closes every subscription. Idempotent. |

`Current`, `Next`, and `Subscribe` return `ErrInvalidConfig` for a nil context rather
than panicking.

### Config

| Field | Type | Default | Meaning |
|---|---|---|---|
| `Accuracy` | `Accuracy` | `AccuracyBalanced` | Power/precision preference passed to the OS. |
| `DesiredAccuracyMeters` | `uint32` | `0` | Explicit accuracy target in metres. Non-zero overrides `Accuracy`. |
| `MinimumInterval` | `time.Duration` | `1s` | Reject a fix arriving sooner than this after the last one. |
| `MinimumDistanceMeters` | `float64` | `0` (off) | Reject a fix closer than this to the last one. |
| `MaximumAge` | `time.Duration` | `2m` | Reject samples older than this. **Zero means "use the default", not "no limit"** — the age check cannot be disabled. |
| `StartTimeout` | `time.Duration` | `30s` | Bounds provider startup and any permission prompt. |
| `Permission` | `PermissionMode` | `PermissionAuto` | Whether `Open` asks the OS for access. |
| `DefaultChannelBuffer` | `int` | `1` | Per-channel capacity for subscriptions that do not set `Buffer`. Must be ≥ 1. |
| `DefaultDropPolicy` | `DropPolicy` | `DropOldest` | Backpressure policy for subscriptions that do not set one. |
| `Linux` | `LinuxConfig` | see below | GeoClue-specific knobs; ignored on other platforms. |

Every field is defaulted independently — setting one never silently clears another. A
zero `Config{}` is equivalent to `DefaultConfig()`.

Both `MinimumInterval` and `MinimumDistanceMeters` are enforced in the common layer on
every platform, and are additionally passed to the native provider where the OS exposes
an equivalent control.

#### LinuxConfig

| Field | Type | Default | Meaning |
|---|---|---|---|
| `DesktopID` | `string` | executable name | The desktop ID GeoClue uses to look up this application's permissions. |
| `Reconnect` | `bool` | `true` | Reconnect automatically when the GeoClue connection drops. |
| `ReconnectMin` | `time.Duration` | `1s` | Initial reconnect backoff. |
| `ReconnectMax` | `time.Duration` | `30s` | Backoff ceiling. Must be ≥ `ReconnectMin`. |

### Subscriptions

```go
type Subscription struct {
    Locations <-chan Fix
    Errors    <-chan error
    Statuses  <-chan Status
}
```

All three channels close together when the subscription's context is done or the locator
closes, so ranging over any of them terminates at shutdown.

| `SubscriptionConfig` field | Type | Default | Meaning |
|---|---|---|---|
| `Buffer` | `int` | `Config.DefaultChannelBuffer` | Per-channel capacity. Must be ≥ 1. |
| `DropPolicy` | `DropPolicy` | `Config.DefaultDropPolicy` | What to discard when a channel is full. |
| `ReplayLatest` | `bool` | `false` | Immediately deliver the newest cached fix, if there is one. |

A subscription never blocks the provider. When a channel is full one value is always
lost, and `DropPolicy` chooses which.

### Fix

One location sample.

| Field | Type | Always set? |
|---|---|---|
| `Timestamp` | `time.Time` | ✅ Provider clock, UTC. |
| `ReceivedAt` | `time.Time` | ✅ Local clock, UTC. |
| `Latitude` | `float64` | ✅ Degrees, −90…90. |
| `Longitude` | `float64` | ✅ Degrees, −180…180. |
| `AccuracyMeters` | `float64` | ✅ Horizontal accuracy. |
| `AltitudeMeters` | `float64` | Only when `Has(FieldAltitude)`. |
| `VerticalAccuracyMeters` | `float64` | Only when `Has(FieldVerticalAccuracy)`. |
| `SpeedMetersPerSecond` | `float64` | Only when `Has(FieldSpeed)`. |
| `HeadingDegrees` | `float64` | Only when `Has(FieldHeading)`. |
| `Source` | `Source` | Reported on Windows only; `SourceUnknown` elsewhere. |
| `Fields` | `Field` | Bitmask of which optional fields are valid. |

| Method | Signature | Notes |
|---|---|---|
| `Has` | `func(field Field) bool` | Whether an optional field carries provider data. |
| `Age` | `func(now time.Time) time.Duration` | Age of the provider timestamp relative to `now`. |

### Status and Capabilities

```go
type Status struct {
    State      State
    Permission PermissionState
    UpdatedAt  time.Time
    Message    string
}

type Capabilities struct {
    Altitude         bool
    VerticalAccuracy bool
    Speed            bool
    Heading          bool
    Source           bool
}
```

### Enumerations

| `Accuracy` | Meaning |
|---|---|
| `AccuracyBalanced` | Default. Lower power, coarser fixes. |
| `AccuracyHigh` | Best accuracy the platform offers. |
| `AccuracyNavigation` | Turn-by-turn profile; highest power draw. |

| `PermissionMode` | Meaning |
|---|---|
| `PermissionAuto` | Default. Ask for access when the platform supports an explicit request. |
| `PermissionRequest` | Always ask. |
| `PermissionDoNotRequest` | Never ask — the host application manages permission itself. |

| `DropPolicy` | Meaning |
|---|---|
| `DropDefault` | Inherit `Config.DefaultDropPolicy`. |
| `DropOldest` | Discard the queued value, keep the newest. |
| `DropNewest` | Keep what is queued, discard the arriving value. |

| `State` | Meaning |
|---|---|
| `StateStarting` | Provider starting, or waiting on a permission prompt. |
| `StateReady` | Fixes are flowing. |
| `StateReconnecting` | Connection lost, retrying (Linux/GeoClue). |
| `StateUnavailable` | Provider reachable but producing nothing. |
| `StateDisabled` | Location services off, or access denied. |
| `StateClosed` | `Close` was called. |

| `PermissionState` | Meaning |
|---|---|
| `PermissionUnknown` | Not yet determined. |
| `PermissionPromptRequired` | The OS needs to ask the user. |
| `PermissionGranted` | Access allowed. |
| `PermissionDenied` | Access refused. |
| `PermissionRestricted` | Blocked by policy (parental controls, MDM). |

| `Source` | Meaning |
|---|---|
| `SourceUnknown` | Not reported. The only value on macOS and Linux. |
| `SourceSystem` | Fused system provider. |
| `SourceSatellite` | GNSS. |
| `SourceWiFi` | Wi-Fi positioning. |
| `SourceCellular` | Cell-tower positioning. |
| `SourceIP` | IP geolocation. |
| `SourceDefault` | Provider default location. |
| `SourceManual` | Set by the user. |
| `SourceRemote` | Supplied by a remote device. |
| `SourceObfuscated` | Deliberately coarsened by the OS. |

| `Field` | Bit for |
|---|---|
| `FieldAltitude` | `AltitudeMeters` |
| `FieldVerticalAccuracy` | `VerticalAccuracyMeters` |
| `FieldSpeed` | `SpeedMetersPerSecond` |
| `FieldHeading` | `HeadingDegrees` |

### Errors

Match with `errors.Is`.

| Sentinel | Raised when |
|---|---|
| `ErrInvalidConfig` | A `Config` or `SubscriptionConfig` value cannot be honoured, or a nil context was passed. |
| `ErrPermissionDenied` | The user or policy refused location access. |
| `ErrPermissionNeeded` | `PermissionDoNotRequest` was set but access has not been granted yet. |
| `ErrServiceDisabled` | Location services are switched off. |
| `ErrServiceUnavailable` | The provider could not be reached or started. |
| `ErrPositionUnavailable` | The provider is running but cannot produce a position. |
| `ErrStaleFix` | A sample arrived older than `MaximumAge`. |
| `ErrClosed` | The locator is closed. |
| `ErrUnsupported` | No native provider exists for this OS/architecture. |

The concrete error is a `*Error` carrying context:

```go
type Error struct {
    Op        string // the operation that failed, e.g. "admit fix"
    Platform  string // "darwin", "linux", "windows"
    Temporary bool   // whether retrying is plausible
    Err       error  // the wrapped cause
}

func (e *Error) Error() string  // "location admit fix (darwin): stale location fix"
func (e *Error) Unwrap() error  // the cause, so errors.Is reaches the sentinels above
```

```go
fix, err := loc.Current(ctx)
if errors.Is(err, location.ErrPermissionDenied) {
    // ask the user to enable location access
}

var locErr *location.Error
if errors.As(err, &locErr) && locErr.Temporary {
    // worth retrying
}
```

### geo helpers

The `geo` package is public and dependency-free if you want the domain values or the
maths on their own:

```go
import "github.com/mostafakhairy0305-dot/golocation/geo"
```

| Symbol | Signature | Notes |
|---|---|---|
| `Distance` | `func(a, b Fix) float64` | Great-circle distance in metres (haversine, IUGG mean radius). |
| `IsFresh` | `func(fix Fix, maxAge time.Duration, now time.Time) bool` | A `maxAge` of zero disables the check. Fixes up to `MaxClockSkew` in the future still count as fresh. |
| `Validate` | `func(fix Fix) error` | Whether a fix carries usable coordinates. Rejects NaN and infinities. |
| `Wrap` | `func(platform, op string, err error, temporary bool) error` | Annotates an error. Returns nil for a nil error. |
| `MaxClockSkew` | `time.Minute` | How far a provider timestamp may run ahead of the local clock and still count as fresh. |

---

## Permissions

Location access is gated by the operating system, and **a denial is reported through
`Status`, not by failing `Open`.** Watch the `Statuses` channel or poll `loc.Status()`:

```go
if s := loc.Status(); s.Permission == location.PermissionDenied {
    // StateDisabled, with s.Message explaining why
}
```

**macOS.** The process must be a signed application bundle with
`NSLocationWhenInUseUsageDescription` in its `Info.plist` to receive the authorization
prompt at all. An unsigned binary — including anything started with `go run` — typically
sits in `StateStarting` with `PermissionPromptRequired` forever. That is an OS
restriction, not a fault in the library.

**Linux.** GeoClue authorizes by desktop ID. Set `Config.Linux.DesktopID` to match your
installed `.desktop` file; the default is the executable's name.

**Windows.** `Open` triggers the WinRT access request unless `Permission` is
`PermissionDoNotRequest`. Location must also be enabled system-wide.

---

## Architecture

The public surface is one package. Behind it the module is organised as a hexagon: every
capability is a feature that declares its own port and ships the adapters implementing it.

```
internal/feature/<name>/port/       the contract, and nothing else
internal/feature/<name>/adapter/*/  one package per implementation
```

| Feature | Responsibility | Adapters |
|---|---|---|
| `admission` | Which samples are publishable | `rules` |
| `fanout` | Delivery, backpressure, one-shot waiters | `chanhub` |
| `fixcache` | The newest admitted fix | `atomiccache` |
| `lifecycle` | Service and permission state | `atomicstate` |
| `clock` | The current time | `systemclock`, `fixedclock` |
| `provider` | The native location source | `corelocation`, `geoclue`, `winrt`, `unsupported` |

The operating system is an adapter like any other: it implements `provider.Provider`, and
`provider.Factory` chooses between implementations. `internal/feature/provider/platform`
supplies the factory bound at build time — the only build-tagged code outside the adapters
themselves.

Run `go doc github.com/mostafakhairy0305-dot/golocation` for the full design notes.

---

## Recommended storage: TimescaleDB

**golocation never touches a database.** It has no storage dependency and no opinion about
where fixes go. But a location stream is textbook time-series data, and when you do want to
persist or sync it server-side, this is what we recommend:

```
timescale/timescaledb-ha:pg18.4-ts2.28.3-all-oss
```

### Running it

```yaml
# compose.yaml
services:
  timescale:
    image: timescale/timescaledb-ha:pg18.4-ts2.28.3-all-oss
    restart: unless-stopped
    environment:
      POSTGRES_DB: golocation
      POSTGRES_USER: golocation
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD}
    ports:
      - "5432:5432"
    volumes:
      # timescaledb-ha uses this PGDATA path, unlike the plain timescaledb image
      - timescale-data:/home/postgres/pgdata/data

volumes:
  timescale-data:
```

```sh
docker compose up -d
docker compose exec timescale psql -U golocation -d golocation -f /dev/stdin < schema.sql
```

### Schema

```sql
CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE fixes (
    time                TIMESTAMPTZ      NOT NULL,  -- Fix.Timestamp (provider clock)
    device_id           TEXT             NOT NULL,
    received_at         TIMESTAMPTZ      NOT NULL,  -- Fix.ReceivedAt (local clock)
    latitude            DOUBLE PRECISION NOT NULL,
    longitude           DOUBLE PRECISION NOT NULL,
    accuracy_m          DOUBLE PRECISION NOT NULL,
    altitude_m          DOUBLE PRECISION,           -- NULL unless Has(FieldAltitude)
    vertical_accuracy_m DOUBLE PRECISION,           -- NULL unless Has(FieldVerticalAccuracy)
    speed_mps           DOUBLE PRECISION,           -- NULL unless Has(FieldSpeed)
    heading_deg         DOUBLE PRECISION,           -- NULL unless Has(FieldHeading)
    source              SMALLINT         NOT NULL,  -- geo.Source ordinal
    UNIQUE (device_id, time)
);
SELECT create_hypertable('fixes', by_range('time'));

CREATE TABLE status_events (
    time       TIMESTAMPTZ NOT NULL,  -- Status.UpdatedAt
    device_id  TEXT        NOT NULL,
    state      SMALLINT    NOT NULL,  -- geo.State ordinal
    permission SMALLINT    NOT NULL,  -- geo.PermissionState ordinal
    message    TEXT        NOT NULL
);
SELECT create_hypertable('status_events', by_range('time'));
CREATE INDEX ON status_events (device_id, time DESC);
```

Store status alongside fixes. When you later find a gap in the fix stream, `status_events`
is what tells you whether it was a denied permission, a disabled service, or a
reconnecting provider.

Four things about this schema that are not obvious from the SQL:

- **The optional columns are nullable because `fix.Has(field)` decides.** Write `NULL`
  when the bit is clear rather than a zero that reads like a real measurement.
  `Capabilities()` tells you which columns can ever be non-NULL on a given platform — on
  macOS and Linux `source` is always `0` (`SourceUnknown`), because neither reports it.

- **`UNIQUE (device_id, time)` plus `ON CONFLICT DO NOTHING` makes re-sync idempotent.**
  That is what lets a client replay a locally buffered batch after an outage without
  worrying about duplicates. The constraint includes `time` because TimescaleDB requires
  every unique constraint on a hypertable to contain the partitioning column.

- **Partitioning on the provider clock is safe here.** A fix's `Timestamp` comes from the
  OS, not from you — but admission has already bounded it before you ever see it:
  `IsFresh` rejects anything older than `MaximumAge` or more than `MaxClockSkew` (one
  minute) in the future, so no sample can open a chunk years ahead. The default chunk
  interval is 7 days; for many devices at once, use
  `create_hypertable('fixes', by_range('time', INTERVAL '1 day'))`.

- **Storing enum ordinals couples the schema to declaration order.** `geo.Source`,
  `geo.State`, and `geo.PermissionState` are `iota` constants, so inserting a new value
  mid-list would silently reinterpret every stored row. If that worries you, map them to
  `TEXT` or a lookup table at write time.

This schema is deliberately minimal — tables, hypertables, indexes. It does not configure
compression or retention, because the right syntax for those has changed across
TimescaleDB releases; check the docs for the version you actually run. The `-all-oss`
variant bundles extra extensions, so run `\dx` in your container to see what is available
(PostGIS, if present, gives you `geography(Point,4326)` and radius queries).

### Writing the stream

Sketch only — this belongs in **your** application, and `pgx` is **your** dependency, not
golocation's.

```go
// Requires: go get github.com/jackc/pgx/v5/pgxpool
func sync(ctx context.Context, pool *pgxpool.Pool, loc location.Locator, deviceID string) error {
    sub, err := loc.Subscribe(ctx, location.SubscriptionConfig{
        Buffer:     256,               // absorb a slow database
        DropPolicy: location.DropOldest,
    })
    if err != nil {
        return err
    }

    flush := time.NewTicker(5 * time.Second)
    defer flush.Stop()

    batch := make([]location.Fix, 0, 256)

    for {
        select {
        case <-ctx.Done():
            return writeFixes(context.WithoutCancel(ctx), pool, deviceID, batch)

        case fix, ok := <-sub.Locations:
            if !ok {
                return writeFixes(context.WithoutCancel(ctx), pool, deviceID, batch)
            }
            batch = append(batch, fix)
            if len(batch) < cap(batch) {
                continue
            }
            if err := writeFixes(ctx, pool, deviceID, batch); err != nil {
                log.Printf("flush failed, retrying next tick: %v", err)
                continue // keep the batch — this is what makes it a sync, not a write
            }
            batch = batch[:0]

        case status, ok := <-sub.Statuses:
            if !ok {
                return nil
            }
            if err := writeStatus(ctx, pool, deviceID, status); err != nil {
                log.Printf("status write failed: %v", err)
            }

        case <-flush.C:
            if len(batch) == 0 {
                continue
            }
            if err := writeFixes(ctx, pool, deviceID, batch); err != nil {
                log.Printf("flush failed, retrying next tick: %v", err)
                continue
            }
            batch = batch[:0]
        }
    }
}
```

The insert maps each optional field through `Has`, and leans on the unique constraint so a
retried batch is harmless:

```go
const insertFix = `
INSERT INTO fixes (time, device_id, received_at, latitude, longitude, accuracy_m,
                   altitude_m, vertical_accuracy_m, speed_mps, heading_deg, source)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (device_id, time) DO NOTHING`

func optional(fix location.Fix, field location.Field, value float64) *float64 {
    if !fix.Has(field) {
        return nil // NULL, not a zero that reads like a measurement
    }
    return &value
}

func writeFixes(ctx context.Context, pool *pgxpool.Pool, deviceID string, fixes []location.Fix) error {
    if len(fixes) == 0 {
        return nil
    }
    batch := &pgx.Batch{}
    for _, fix := range fixes {
        batch.Queue(insertFix,
            fix.Timestamp, deviceID, fix.ReceivedAt,
            fix.Latitude, fix.Longitude, fix.AccuracyMeters,
            optional(fix, location.FieldAltitude, fix.AltitudeMeters),
            optional(fix, location.FieldVerticalAccuracy, fix.VerticalAccuracyMeters),
            optional(fix, location.FieldSpeed, fix.SpeedMetersPerSecond),
            optional(fix, location.FieldHeading, fix.HeadingDegrees),
            int16(fix.Source),
        )
    }
    return pool.SendBatch(ctx, batch).Close()
}
```

Two details worth copying: the batch is **kept, not dropped**, when a flush fails — that
is the difference between storing and syncing — and the final flush uses
`context.WithoutCancel` so a cancelled context still gets its buffered fixes written.

---

## License

MIT. See [LICENSE](LICENSE).
