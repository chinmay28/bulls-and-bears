package momentum

import (
	"testing"

	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/paritytest"
)

// The sector_rotation study emits this same strategy on the sector ETFs; its
// goldens are a second parity check on the implementation, over a universe
// of nine risk assets and a next-open fill.
func TestParitySectorRotation(t *testing.T) {
	paritytest.Run(t, "../../../../golden/sector_rotation", Name,
		func(u []string, p strategy.Params, s strategy.Sizing) (paritytest.Weighter, error) { return Parse(u, p, s) })
}
