package risk

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds every threshold of docs/PLAN.md §6 that is the operator's to
// set. It is read from risk.yaml; the defaults are the plan's. The two
// limits a strategy carries with it — per-leg notional and gross leverage —
// are Limits, not Config, because they are part of what was backtested.
//
// A field left out of the file, or set to zero, takes its default. There is
// no way to switch a rule off from the file: a rule that is not wanted gets a
// threshold it cannot reach, and a rule left out by accident still guards.
type Config struct {
	// MaxOrdersPerDay caps orders sent in one trading day, across symbols.
	MaxOrdersPerDay int `yaml:"max_orders_per_day"`
	// DailyLossLimit is the loss, as a fraction of start-of-day equity, from
	// which no order may open a position for the rest of the day. Closes
	// are still allowed: a losing day is no reason to be stuck in it.
	DailyLossLimit float64 `yaml:"daily_loss_limit"`
	// DrawdownKillSwitch is the drawdown from the equity high-water mark, as
	// a fraction, at which trading halts until `bnb resume`.
	DrawdownKillSwitch float64 `yaml:"drawdown_kill_switch"`
	// ConsecutiveDailyLimitHits is how many trading days in a row the daily
	// loss limit may be hit before trading halts for the rest of the week.
	ConsecutiveDailyLimitHits int `yaml:"consecutive_daily_limit_hits"`
	// StaleQuote is the age past which a quote cannot back an order. Written
	// as a duration: "15m".
	StaleQuote time.Duration `yaml:"stale_quote"`
	// DivergenceWarn and DivergenceHalt are the gaps between paper and live
	// equity, as fractions of paper equity, that warn and that halt.
	DivergenceWarn float64 `yaml:"divergence_warn"`
	DivergenceHalt float64 `yaml:"divergence_halt"`
	// ConfirmAboveUSD is the single-order notional above which a live order
	// needs a person's confirmation unless the run was started with --yes.
	// Dry-run never asks.
	ConfirmAboveUSD float64 `yaml:"confirm_above_usd"`
}

// DefaultConfig is the §6 table.
func DefaultConfig() Config {
	return Config{
		MaxOrdersPerDay:           10,
		DailyLossLimit:            0.03,
		DrawdownKillSwitch:        0.10,
		ConsecutiveDailyLimitHits: 3,
		StaleQuote:                15 * time.Minute,
		DivergenceWarn:            0.02,
		DivergenceHalt:            0.05,
		ConfirmAboveUSD:           500,
	}
}

// LoadConfig reads a risk.yaml. A key the Config does not have is an error
// rather than ignored, so a misspelt threshold cannot silently leave the
// default in force. Missing and zero fields take defaults; a negative or
// impossible value is an error.
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	cfg, err := parseConfig(f)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func parseConfig(r io.Reader) (Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	// An empty file is a file that sets nothing, which is every default.
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, err
	}
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// withDefaults fills every zero field from DefaultConfig.
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.MaxOrdersPerDay == 0 {
		c.MaxOrdersPerDay = d.MaxOrdersPerDay
	}
	if c.DailyLossLimit == 0 {
		c.DailyLossLimit = d.DailyLossLimit
	}
	if c.DrawdownKillSwitch == 0 {
		c.DrawdownKillSwitch = d.DrawdownKillSwitch
	}
	if c.ConsecutiveDailyLimitHits == 0 {
		c.ConsecutiveDailyLimitHits = d.ConsecutiveDailyLimitHits
	}
	if c.StaleQuote == 0 {
		c.StaleQuote = d.StaleQuote
	}
	if c.DivergenceWarn == 0 {
		c.DivergenceWarn = d.DivergenceWarn
	}
	if c.DivergenceHalt == 0 {
		c.DivergenceHalt = d.DivergenceHalt
	}
	if c.ConfirmAboveUSD == 0 {
		c.ConfirmAboveUSD = d.ConfirmAboveUSD
	}
	return c
}

// Validate says whether every threshold is one the rules can apply. The
// fractions must sit in (0, 1]: "3" for the daily loss limit is a
// percentage typed where a fraction belongs, and a rule that can never fire
// is worse than a loud error at start-up.
func (c Config) Validate() error {
	if c.MaxOrdersPerDay < 0 {
		return fmt.Errorf("max_orders_per_day %d is negative", c.MaxOrdersPerDay)
	}
	if c.ConsecutiveDailyLimitHits < 0 {
		return fmt.Errorf("consecutive_daily_limit_hits %d is negative", c.ConsecutiveDailyLimitHits)
	}
	if c.StaleQuote < 0 {
		return fmt.Errorf("stale_quote %s is negative", c.StaleQuote)
	}
	for _, f := range []struct {
		name string
		v    float64
	}{
		{"daily_loss_limit", c.DailyLossLimit},
		{"drawdown_kill_switch", c.DrawdownKillSwitch},
		{"divergence_warn", c.DivergenceWarn},
		{"divergence_halt", c.DivergenceHalt},
	} {
		if math.IsNaN(f.v) || f.v <= 0 || f.v > 1 {
			return fmt.Errorf("%s %v is not a fraction in (0, 1]: write 0.03 for 3%%", f.name, f.v)
		}
	}
	if c.DivergenceWarn > c.DivergenceHalt {
		return fmt.Errorf("divergence_warn %v is above divergence_halt %v", c.DivergenceWarn, c.DivergenceHalt)
	}
	if math.IsNaN(c.ConfirmAboveUSD) || c.ConfirmAboveUSD < 0 {
		return fmt.Errorf("confirm_above_usd %v is not a dollar amount", c.ConfirmAboveUSD)
	}
	return nil
}
