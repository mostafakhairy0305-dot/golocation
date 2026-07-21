// Package atomiccache implements fixcache.Cache with a single atomic pointer.
package atomiccache

import (
	"sync/atomic"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	fixcache "github.com/mostafakhairy0305-dot/golocation/internal/feature/fixcache/port"
)

// Cache is the default fixcache.Cache. Load is a single atomic load, so
// readers never block each other or the writer — worth one small allocation
// per stored fix, since fixes arrive about once a second while Last can be
// called from every subscriber at once.
type Cache struct {
	latest atomic.Pointer[geo.Fix]
}

var _ fixcache.Cache = (*Cache)(nil)

func New() *Cache { return &Cache{} }

// Store publishes fix as the newest value. It stores the address of the
// parameter, which is a copy the caller cannot reach, so the Fix behind the
// published pointer is immutable by construction.
func (c *Cache) Store(fix geo.Fix) { c.latest.Store(&fix) }

func (c *Cache) Load() (geo.Fix, bool) {
	latest := c.latest.Load()
	if latest == nil {
		return geo.Fix{}, false
	}

	return *latest, true
}
