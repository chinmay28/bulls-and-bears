// Package strategy is the pure function at the centre of the runtime.
//
// Targets(bars, positions) -> target weights. No I/O, no clock, no
// randomness: that is what lets the backtest, the dry-run and live trading
// share one code path, and what makes the Go implementation checkable against
// the Python one row for row (docs/PLAN.md §4.3). Implementations register
// under the name a spec's `strategy` field uses.
package strategy

import (
	"fmt"
	"sort"
	"sync"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
)

// Position is what the broker says is held: signed quantity, average cost.
type Position struct {
	Symbol  string
	Qty     float64
	AvgCost float64
}

// Strategy turns history into target weights.
type Strategy interface {
	// Targets returns the target weight (fraction of equity, signed) per
	// symbol as of the last bar in hist. hist holds every symbol in
	// Universe, oldest first. A symbol with no target is flat. Pure.
	Targets(hist map[string][]bars.Bar, pos []Position) (map[string]float64, error)
	Universe() []string
}

// Params is a spec's params block: name -> number, as the schema allows.
type Params map[string]float64

// Sizing is a spec's sizing block.
type Sizing struct {
	GrossLeverage        float64
	MaxNotionalPerLegUSD float64
}

// Factory builds a Strategy from a spec's universe, params and sizing,
// returning an error for a universe or parameter set the strategy cannot use.
type Factory func(universe []string, params Params, sizing Sizing) (Strategy, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register makes a strategy available under the name specs use. Registering
// a name twice is a programming error and panics at init.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[name]; dup {
		panic("strategy: " + name + " registered twice")
	}
	registry[name] = f
}

// New builds the named strategy. An unknown name is an error, not a default:
// a spec naming a strategy the runtime does not have is a spec it must refuse.
func New(name string, universe []string, params Params, sizing Sizing) (Strategy, error) {
	mu.RLock()
	f, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("strategy: no implementation registered as %q (have %v)", name, Names())
	}
	return f(universe, params, sizing)
}

// Names lists the registered strategies, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
