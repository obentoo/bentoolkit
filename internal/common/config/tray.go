package config

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	yaml "go.yaml.in/yaml/v3"
)

const (
	// DefaultTrayInterval is how often bentoo-tray checks the feed when
	// tray.interval is absent.
	DefaultTrayInterval = 6 * time.Hour
	// MinTrayInterval is the floor tray.interval is clamped to.
	MinTrayInterval = time.Hour
	// DefaultTrayFeedURL is the notices feed bentoo-tray reads by default.
	DefaultTrayFeedURL = "https://obentoo.org/notices.json"
)

// mutableNoticeTypes are the notice types tray.mute may silence. "security" is
// deliberately absent: a security notice can never be muted.
var mutableNoticeTypes = []string{"release", "news", "announcement"}

// TrayConfig is the `tray:` block read by bentoo-tray. The getters
// apply the defaults and return their warnings as strings: this package does
// not log, the caller does.
type TrayConfig struct {
	// Interval is a time.ParseDuration string; empty means DefaultTrayInterval.
	Interval string `yaml:"interval,omitempty"`
	// FeedURL is the notices feed; empty means DefaultTrayFeedURL.
	FeedURL string `yaml:"feed_url,omitempty"`
	// Mute lists notice types that are recorded unread but never notified.
	Mute []string `yaml:"mute,omitempty"`
	// DowngradeCritical sends critical notices at normal urgency.
	DowngradeCritical bool `yaml:"downgrade_critical,omitempty"`
	// SkipMetered is a pointer so an explicit false stays distinct from an
	// absent key, whose default is true.
	SkipMetered *bool `yaml:"skip_metered,omitempty"`
	// Monitors is the raw system health section, kept as a node so a section
	// of the wrong shape reaches GetMonitors as a warning instead of failing
	// the whole config load. GetMonitors decodes it into MonitorsConfig.
	Monitors yaml.Node `yaml:"monitors,omitempty"`
}

// GetInterval returns the check interval: DefaultTrayInterval when unset or
// unparsable, and never below MinTrayInterval. Each correction comes back as a
// warning naming the configured value.
func (t TrayConfig) GetInterval() (time.Duration, []string) {
	if t.Interval == "" {
		return DefaultTrayInterval, nil
	}
	d, err := time.ParseDuration(t.Interval)
	if err != nil {
		return DefaultTrayInterval, []string{fmt.Sprintf(
			"tray.interval %q is not a duration; using %s", t.Interval, DefaultTrayInterval)}
	}
	if d < MinTrayInterval {
		return MinTrayInterval, []string{fmt.Sprintf(
			"tray.interval %q is below the %s minimum; using %s", t.Interval, MinTrayInterval, MinTrayInterval)}
	}
	return d, nil
}

// GetFeedURL returns the configured feed URL or DefaultTrayFeedURL.
func (t TrayConfig) GetFeedURL() string {
	if t.FeedURL == "" {
		return DefaultTrayFeedURL
	}
	return t.FeedURL
}

// GetMute returns the mutable notice types listed in tray.mute, in their
// configured order. "security" and unknown types (matched case-sensitively)
// are dropped, one warning each.
func (t TrayConfig) GetMute() (types []string, warnings []string) {
	for _, typ := range t.Mute {
		switch {
		case typ == "security":
			warnings = append(warnings, "tray.mute: security notices cannot be muted; ignoring \"security\"")
		case !slices.Contains(mutableNoticeTypes, typ):
			warnings = append(warnings, fmt.Sprintf(
				"tray.mute: unknown notice type %q ignored (known: release, news, announcement)", typ))
		default:
			types = append(types, typ)
		}
	}
	return types, warnings
}

// GetSkipMetered reports whether checks are skipped on a metered connection;
// true unless tray.skip_metered is explicitly false.
func (t TrayConfig) GetSkipMetered() bool {
	return t.SkipMetered == nil || *t.SkipMetered
}

// Defaults and limits of the tray.monitors keys. Sizes are bytes.
const (
	DefaultMonitorSampleInterval = 30 * time.Second
	MinMonitorSampleInterval     = 10 * time.Second
	DefaultMonitorReminder       = time.Hour
	MinMonitorReminder           = 15 * time.Minute

	DefaultDiskWarnPercent        = 10.0
	DefaultDiskWarnFree    uint64 = 20 << 30
	DefaultDiskCritPercent        = 2.0
	DefaultDiskCritFree    uint64 = 2 << 30
	DefaultDiskSustain            = time.Minute

	DefaultMemoryWarnPercent     = 10.0
	DefaultMemoryCritPercent     = 5.0
	DefaultMemoryCritSwapPercent = 10.0
	DefaultMemoryPSIFullCrit     = 10.0
	DefaultMemorySustain         = 2 * time.Minute

	DefaultCPUWarnPSI  = 80.0
	DefaultCPUClearPSI = 70.0
	DefaultCPUSustain  = 5 * time.Minute

	DefaultNetworkDownAfter        = 30 * time.Second
	DefaultNetworkDownAfterStartup = time.Minute
	DefaultNetworkBackAfter        = 10 * time.Second
)

// DefaultEmergeLog is the emerge log the emerge monitor follows.
const DefaultEmergeLog = "/var/log/emerge.log"

const (
	// DefaultSyncMaxAge is how old a repository's last sync may grow before
	// the sync monitor reports it.
	DefaultSyncMaxAge = 7 * 24 * time.Hour
	// MinSyncMaxAge is the shortest tray.monitors.sync.max_age accepted.
	MinSyncMaxAge = 24 * time.Hour
)

// MonitorsConfig is the decoded `tray.monitors:` section. Every leaf and
// every block is a yaml.Node, so an absent key stays distinct from a zero and
// a value of the wrong YAML type ("10%", "maybe") or a block of the wrong
// shape (`disk: 5`) reaches GetMonitors as a warning instead of failing the
// whole config load. Each block decodes into its own *MonitorConfig type.
type MonitorsConfig struct {
	SampleInterval yaml.Node `yaml:"sample_interval,omitempty"`
	Reminder       yaml.Node `yaml:"reminder,omitempty"`
	Disk           yaml.Node `yaml:"disk,omitempty"`
	Memory         yaml.Node `yaml:"memory,omitempty"`
	CPU            yaml.Node `yaml:"cpu,omitempty"`
	Network        yaml.Node `yaml:"network,omitempty"`
	Emerge         yaml.Node `yaml:"emerge,omitempty"`
	Units          yaml.Node `yaml:"units,omitempty"`
	Readonly       yaml.Node `yaml:"readonly,omitempty"`
	Smart          yaml.Node `yaml:"smart,omitempty"`
	Updates        yaml.Node `yaml:"updates,omitempty"`
	GLSA           yaml.Node `yaml:"glsa,omitempty"`
	Sync           yaml.Node `yaml:"sync,omitempty"`
}

// DiskMonitorConfig is the raw `tray.monitors.disk:` block.
type DiskMonitorConfig struct {
	Enabled     yaml.Node `yaml:"enabled,omitempty"`
	Paths       yaml.Node `yaml:"paths,omitempty"`
	WarnPercent yaml.Node `yaml:"warn_percent,omitempty"`
	WarnFree    yaml.Node `yaml:"warn_free,omitempty"`
	CritPercent yaml.Node `yaml:"crit_percent,omitempty"`
	CritFree    yaml.Node `yaml:"crit_free,omitempty"`
	Sustain     yaml.Node `yaml:"sustain,omitempty"`
}

// MemoryMonitorConfig is the raw `tray.monitors.memory:` block.
type MemoryMonitorConfig struct {
	Enabled         yaml.Node `yaml:"enabled,omitempty"`
	WarnPercent     yaml.Node `yaml:"warn_percent,omitempty"`
	CritPercent     yaml.Node `yaml:"crit_percent,omitempty"`
	CritSwapPercent yaml.Node `yaml:"crit_swap_percent,omitempty"`
	PSIFullCrit     yaml.Node `yaml:"psi_full_crit,omitempty"`
	Sustain         yaml.Node `yaml:"sustain,omitempty"`
}

// CPUMonitorConfig is the raw `tray.monitors.cpu:` block.
type CPUMonitorConfig struct {
	Enabled  yaml.Node `yaml:"enabled,omitempty"`
	WarnPSI  yaml.Node `yaml:"warn_psi,omitempty"`
	ClearPSI yaml.Node `yaml:"clear_psi,omitempty"`
	Sustain  yaml.Node `yaml:"sustain,omitempty"`
}

// NetworkMonitorConfig is the raw `tray.monitors.network:` block.
type NetworkMonitorConfig struct {
	Enabled          yaml.Node `yaml:"enabled,omitempty"`
	DownAfter        yaml.Node `yaml:"down_after,omitempty"`
	DownAfterStartup yaml.Node `yaml:"down_after_startup,omitempty"`
	BackAfter        yaml.Node `yaml:"back_after,omitempty"`
}

// EmergeMonitorConfig is the raw `tray.monitors.emerge:` block.
type EmergeMonitorConfig struct {
	Enabled yaml.Node `yaml:"enabled,omitempty"`
	Log     yaml.Node `yaml:"log,omitempty"`
}

// UnitsMonitorConfig is the raw `tray.monitors.units:` block.
type UnitsMonitorConfig struct {
	Enabled yaml.Node `yaml:"enabled,omitempty"`
	System  yaml.Node `yaml:"system,omitempty"`
	User    yaml.Node `yaml:"user,omitempty"`
}

// ReadonlyMonitorConfig is the raw `tray.monitors.readonly:` block.
type ReadonlyMonitorConfig struct {
	Enabled yaml.Node `yaml:"enabled,omitempty"`
}

// SmartMonitorConfig is the raw `tray.monitors.smart:` block.
type SmartMonitorConfig struct {
	Enabled yaml.Node `yaml:"enabled,omitempty"`
}

// UpdatesMonitorConfig is the raw `tray.monitors.updates:` block.
type UpdatesMonitorConfig struct {
	Enabled yaml.Node `yaml:"enabled,omitempty"`
}

// GLSAMonitorConfig is the raw `tray.monitors.glsa:` block.
type GLSAMonitorConfig struct {
	Enabled yaml.Node `yaml:"enabled,omitempty"`
}

// SyncMonitorConfig is the raw `tray.monitors.sync:` block.
type SyncMonitorConfig struct {
	Enabled yaml.Node `yaml:"enabled,omitempty"`
	MaxAge  yaml.Node `yaml:"max_age,omitempty"`
}

// Monitors is the resolved tray.monitors block: every value parsed, checked
// against its limits and defaulted.
type Monitors struct {
	SampleInterval time.Duration
	Reminder       time.Duration
	Disk           DiskMonitor
	Memory         MemoryMonitor
	CPU            CPUMonitor
	Network        NetworkMonitor
	Emerge         EmergeMonitor
	Units          UnitsMonitor
	Readonly       ReadonlyMonitor
	Smart          SmartMonitor
	Updates        UpdatesMonitor
	GLSA           GLSAMonitor
	Sync           SyncMonitor
}

// DiskMonitor holds the disk thresholds. Paths is empty unless configured;
// the caller resolves the default paths. Percentages are of the filesystem
// size; WarnFree and CritFree are bytes.
type DiskMonitor struct {
	Enabled     bool
	Paths       []string
	WarnPercent float64
	WarnFree    uint64
	CritPercent float64
	CritFree    uint64
	Sustain     time.Duration
}

// MemoryMonitor holds the memory thresholds, in percent of total memory or
// swap; PSIFullCrit is the "full avg60" pressure percentage.
type MemoryMonitor struct {
	Enabled         bool
	WarnPercent     float64
	CritPercent     float64
	CritSwapPercent float64
	PSIFullCrit     float64
	Sustain         time.Duration
}

// CPUMonitor holds the CPU pressure thresholds; ClearPSI is always below
// WarnPSI when WarnPSI is above zero.
type CPUMonitor struct {
	Enabled  bool
	WarnPSI  float64
	ClearPSI float64
	Sustain  time.Duration
}

// EmergeMonitor names the emerge log whose failed builds are announced.
type EmergeMonitor struct {
	Enabled bool
	Log     string
}

// UnitsMonitor selects the systemd managers whose failed units are
// announced.
type UnitsMonitor struct {
	Enabled bool
	System  bool
	User    bool
}

// ReadonlyMonitor switches the read-only remount monitor.
type ReadonlyMonitor struct {
	Enabled bool
}

// SmartMonitor switches the udisks2 disk health monitor.
type SmartMonitor struct {
	Enabled bool
}

// UpdatesMonitor switches the pending world updates monitor.
type UpdatesMonitor struct {
	Enabled bool
}

// GLSAMonitor switches the security advisory monitor.
type GLSAMonitor struct {
	Enabled bool
}

// SyncMonitor holds how old a repository's last sync may grow before it is
// reported; MaxAge is never below MinSyncMaxAge.
type SyncMonitor struct {
	Enabled bool
	MaxAge  time.Duration
}

// NetworkMonitor holds the network debounce windows.
type NetworkMonitor struct {
	Enabled          bool
	DownAfter        time.Duration
	DownAfterStartup time.Duration
	BackAfter        time.Duration
}

// GetMonitors resolves tray.monitors. An absent key takes its default; an
// unparsable or out-of-range value takes its default too and comes back as
// one warning naming the full key and the configured value. A section or
// block that is not a mapping warns the same way and takes the defaults of
// everything inside it; a null one is absent.
func (t TrayConfig) GetMonitors() (Monitors, []string) {
	var r monitorResolver
	c := decodeBlock[MonitorsConfig](&r, "", &t.Monitors)
	disk := decodeBlock[DiskMonitorConfig](&r, "disk", &c.Disk)
	memory := decodeBlock[MemoryMonitorConfig](&r, "memory", &c.Memory)
	cpu := decodeBlock[CPUMonitorConfig](&r, "cpu", &c.CPU)
	network := decodeBlock[NetworkMonitorConfig](&r, "network", &c.Network)
	emerge := decodeBlock[EmergeMonitorConfig](&r, "emerge", &c.Emerge)
	units := decodeBlock[UnitsMonitorConfig](&r, "units", &c.Units)
	readonly := decodeBlock[ReadonlyMonitorConfig](&r, "readonly", &c.Readonly)
	smart := decodeBlock[SmartMonitorConfig](&r, "smart", &c.Smart)
	updates := decodeBlock[UpdatesMonitorConfig](&r, "updates", &c.Updates)
	glsa := decodeBlock[GLSAMonitorConfig](&r, "glsa", &c.GLSA)
	sync := decodeBlock[SyncMonitorConfig](&r, "sync", &c.Sync)
	m := Monitors{
		SampleInterval: r.duration("sample_interval", &c.SampleInterval, DefaultMonitorSampleInterval, MinMonitorSampleInterval),
		Reminder:       r.duration("reminder", &c.Reminder, DefaultMonitorReminder, MinMonitorReminder),
		Disk:           r.disk(&disk),
		Memory:         r.memory(&memory),
		CPU:            r.cpu(&cpu),
		Network:        r.network(&network),
		Emerge:         r.emerge(&emerge),
		Units:          r.units(&units),
		Readonly:       ReadonlyMonitor{Enabled: r.enabled("readonly.enabled", &readonly.Enabled)},
		Smart:          SmartMonitor{Enabled: r.enabled("smart.enabled", &smart.Enabled)},
		Updates:        UpdatesMonitor{Enabled: r.enabled("updates.enabled", &updates.Enabled)},
		GLSA:           GLSAMonitor{Enabled: r.enabled("glsa.enabled", &glsa.Enabled)},
		Sync:           r.sync(&sync),
	}
	return m, r.warnings
}

// monitorKey is the full path of key, which is relative to tray.monitors; an
// empty key names the section itself.
func monitorKey(key string) string {
	if key == "" {
		return "tray.monitors"
	}
	return "tray.monitors." + key
}

// decodeBlock decodes the block n into a T. An absent or null block yields
// the zero T, whose leaves all take their defaults; a block that is not a
// mapping, or does not decode, yields the zero T plus one warning naming the
// block and its value.
func decodeBlock[T any](r *monitorResolver, key string, n *yaml.Node) T {
	var c T
	n, ok := presentNode(n)
	if !ok {
		return c
	}
	if n.Kind != yaml.MappingNode {
		r.warnings = append(r.warnings, fmt.Sprintf(
			"%s %q is not a mapping; using its defaults", monitorKey(key), nodeText(n)))
		return c
	}
	if err := n.Decode(&c); err != nil {
		r.warnings = append(r.warnings, fmt.Sprintf(
			"%s %q cannot be read (%v); using its defaults", monitorKey(key), nodeText(n), err))
		var zero T
		return zero
	}
	return c
}

// monitorResolver collects the warnings of one GetMonitors call. Its key
// arguments are relative to tray.monitors.
type monitorResolver struct {
	warnings []string
}

// resolveKey returns def when n is absent or null, the parsed value when it
// parses, and def plus a warning naming the key and the value otherwise.
func resolveKey[T any](r *monitorResolver, key string, n *yaml.Node, def T,
	parse func(*yaml.Node) (T, error), show func(T) string) T {
	n, ok := presentNode(n)
	if !ok {
		return def
	}
	v, err := parse(n)
	if err != nil {
		r.warnings = append(r.warnings, fmt.Sprintf(
			"%s %q %v; using %s", monitorKey(key), nodeText(n), err, show(def)))
		return def
	}
	return v
}

// enabled resolves a monitor's on/off switch, which defaults to on.
func (r *monitorResolver) enabled(key string, n *yaml.Node) bool {
	return resolveKey(r, key, n, true, parseEnabled, strconv.FormatBool)
}

func (r *monitorResolver) percent(key string, n *yaml.Node, def float64) float64 {
	return resolveKey(r, key, n, def, parsePercent, showFloat)
}

func (r *monitorResolver) size(key string, n *yaml.Node, def uint64) uint64 {
	return resolveKey(r, key, n, def, parseSizeNode, formatSize)
}

func (r *monitorResolver) duration(key string, n *yaml.Node, def, lowest time.Duration) time.Duration {
	return resolveKey(r, key, n, def, durationAtLeast(lowest), time.Duration.String)
}

func (r *monitorResolver) disk(c *DiskMonitorConfig) DiskMonitor {
	return DiskMonitor{
		Enabled:     r.enabled("disk.enabled", &c.Enabled),
		Paths:       resolveKey(r, "disk.paths", &c.Paths, nil, parsePaths, showDefaultPaths),
		WarnPercent: r.percent("disk.warn_percent", &c.WarnPercent, DefaultDiskWarnPercent),
		WarnFree:    r.size("disk.warn_free", &c.WarnFree, DefaultDiskWarnFree),
		CritPercent: r.percent("disk.crit_percent", &c.CritPercent, DefaultDiskCritPercent),
		CritFree:    r.size("disk.crit_free", &c.CritFree, DefaultDiskCritFree),
		Sustain:     r.duration("disk.sustain", &c.Sustain, DefaultDiskSustain, 0),
	}
}

func (r *monitorResolver) memory(c *MemoryMonitorConfig) MemoryMonitor {
	return MemoryMonitor{
		Enabled:         r.enabled("memory.enabled", &c.Enabled),
		WarnPercent:     r.percent("memory.warn_percent", &c.WarnPercent, DefaultMemoryWarnPercent),
		CritPercent:     r.percent("memory.crit_percent", &c.CritPercent, DefaultMemoryCritPercent),
		CritSwapPercent: r.percent("memory.crit_swap_percent", &c.CritSwapPercent, DefaultMemoryCritSwapPercent),
		PSIFullCrit:     r.percent("memory.psi_full_crit", &c.PSIFullCrit, DefaultMemoryPSIFullCrit),
		Sustain:         r.duration("memory.sustain", &c.Sustain, DefaultMemorySustain, 0),
	}
}

// cpu resolves warn_psi first, then clear_psi against it: a clear level at or
// above the warn level could never clear, so it is refused like any bad value.
func (r *monitorResolver) cpu(c *CPUMonitorConfig) CPUMonitor {
	on := r.enabled("cpu.enabled", &c.Enabled)
	warn := r.percent("cpu.warn_psi", &c.WarnPSI, DefaultCPUWarnPSI)
	belowWarn := func(n *yaml.Node) (float64, error) {
		v, err := parsePercent(n)
		if err != nil {
			return 0, err
		}
		if v >= warn {
			return 0, fmt.Errorf("is not below tray.monitors.cpu.warn_psi (%s)", showFloat(warn))
		}
		return v, nil
	}
	return CPUMonitor{
		Enabled:  on,
		WarnPSI:  warn,
		ClearPSI: resolveKey(r, "cpu.clear_psi", &c.ClearPSI, defaultClearPSI(warn), belowWarn, showFloat),
		Sustain:  r.duration("cpu.sustain", &c.Sustain, DefaultCPUSustain, 0),
	}
}

func (r *monitorResolver) network(c *NetworkMonitorConfig) NetworkMonitor {
	return NetworkMonitor{
		Enabled:          r.enabled("network.enabled", &c.Enabled),
		DownAfter:        r.duration("network.down_after", &c.DownAfter, DefaultNetworkDownAfter, 0),
		DownAfterStartup: r.duration("network.down_after_startup", &c.DownAfterStartup, DefaultNetworkDownAfterStartup, 0),
		BackAfter:        r.duration("network.back_after", &c.BackAfter, DefaultNetworkBackAfter, 0),
	}
}

func (r *monitorResolver) emerge(c *EmergeMonitorConfig) EmergeMonitor {
	return EmergeMonitor{
		Enabled: r.enabled("emerge.enabled", &c.Enabled),
		Log:     resolveKey(r, "emerge.log", &c.Log, DefaultEmergeLog, parseAbsPath, strconv.Quote),
	}
}

func (r *monitorResolver) units(c *UnitsMonitorConfig) UnitsMonitor {
	return UnitsMonitor{
		Enabled: r.enabled("units.enabled", &c.Enabled),
		System:  r.enabled("units.system", &c.System),
		User:    r.enabled("units.user", &c.User),
	}
}

func (r *monitorResolver) sync(c *SyncMonitorConfig) SyncMonitor {
	return SyncMonitor{
		Enabled: r.enabled("sync.enabled", &c.Enabled),
		MaxAge:  r.duration("sync.max_age", &c.MaxAge, DefaultSyncMaxAge, MinSyncMaxAge),
	}
}

// defaultClearPSI is DefaultCPUClearPSI while that stays below warn; under a
// lower warn level it keeps the default pair's proportion (70/80 of warn).
func defaultClearPSI(warn float64) float64 {
	if DefaultCPUClearPSI < warn {
		return DefaultCPUClearPSI
	}
	return warn * DefaultCPUClearPSI / DefaultCPUWarnPSI
}

// unknownMonitorKeys lists every key under tray.monitors that no field reads,
// by its full path and line ("line 5: unknown key tray.monitors.disk.bogus").
// The strict re-decode in LoadFrom cannot see inside the raw section, so this
// walk stands in for it there. Blocks that are not mappings are skipped:
// GetMonitors already reports them. A YAML merge key ("<<") is accepted and
// its merged keys are not checked.
func (t TrayConfig) unknownMonitorKeys() []string {
	sec, ok := presentNode(&t.Monitors)
	if !ok || sec.Kind != yaml.MappingNode {
		return nil
	}
	blocks := map[string]reflect.Type{
		"disk":     reflect.TypeFor[DiskMonitorConfig](),
		"memory":   reflect.TypeFor[MemoryMonitorConfig](),
		"cpu":      reflect.TypeFor[CPUMonitorConfig](),
		"network":  reflect.TypeFor[NetworkMonitorConfig](),
		"emerge":   reflect.TypeFor[EmergeMonitorConfig](),
		"units":    reflect.TypeFor[UnitsMonitorConfig](),
		"readonly": reflect.TypeFor[ReadonlyMonitorConfig](),
		"smart":    reflect.TypeFor[SmartMonitorConfig](),
		"updates":  reflect.TypeFor[UpdatesMonitorConfig](),
		"glsa":     reflect.TypeFor[GLSAMonitorConfig](),
		"sync":     reflect.TypeFor[SyncMonitorConfig](),
	}
	unknown := unknownKeys("", sec, reflect.TypeFor[MonitorsConfig]())
	for i := 0; i+1 < len(sec.Content); i += 2 {
		key := sec.Content[i].Value
		typ, isBlock := blocks[key]
		if !isBlock {
			continue
		}
		if block, ok := presentNode(sec.Content[i+1]); ok && block.Kind == yaml.MappingNode {
			unknown = append(unknown, unknownKeys(key, block, typ)...)
		}
	}
	return unknown
}

// unknownKeys lists the keys of the mapping n, which sits at key under
// tray.monitors, that the struct typ has no yaml field for. An alias key is
// read as its target, as the decoder reads it.
func unknownKeys(key string, n *yaml.Node, typ reflect.Type) []string {
	known := yamlKeys(typ)
	var unknown []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.ShortTag() == "!!merge" {
			continue
		}
		line := k.Line
		for k.Kind == yaml.AliasNode && k.Alias != nil {
			k = k.Alias
		}
		// A key that is not text fails the block's decode, which already warns.
		if k.Kind != yaml.ScalarNode || slices.Contains(known, k.Value) {
			continue
		}
		unknown = append(unknown, fmt.Sprintf("line %d: unknown key %s.%s", line, monitorKey(key), escapeControl(k.Value)))
	}
	return unknown
}

// escapeControl writes control characters in s as Go escapes, so a key read
// from the file cannot start a forged line or a terminal sequence on stderr.
func escapeControl(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	q := strconv.Quote(s)
	return q[1 : len(q)-1]
}

// yamlKeys lists the keys the struct typ decodes, as yaml.v3 names them: the
// tag's name, or the lower-cased field name when the tag carries none.
func yamlKeys(typ reflect.Type) []string {
	keys := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch {
		case name == "-" || !f.IsExported():
			continue
		case name == "":
			name = strings.ToLower(f.Name)
		}
		keys = append(keys, name)
	}
	return keys
}

// maxSavedMonitorNodes bounds how many nodes resolving the monitors node's
// aliases may produce, so a few nested aliases cannot expand into a file of
// gigabytes on save.
const maxSavedMonitorNodes = 100_000

// errMonitorsTooLarge reports a monitors node whose resolved copy would pass
// maxSavedMonitorNodes.
var errMonitorsTooLarge = errors.New("tray.monitors expands past the size a saved configuration may have")

// resolvedCopy returns a deep copy of n with every alias replaced by a copy of
// its target and every anchor cleared, so the copy marshals on its own. The
// parser refuses self-referencing anchors, so the walk is finite; its size is
// bounded by maxSavedMonitorNodes.
func resolvedCopy(n *yaml.Node) (yaml.Node, error) {
	budget := maxSavedMonitorNodes
	return resolveNode(n, false, &budget)
}

// resolveNode copies n for resolvedCopy. isKey marks a mapping key: a key
// written as an alias of a merge key is not a merge key to the decoder, so its
// copy is a quoted string, which the decoder does not merge either.
func resolveNode(n *yaml.Node, isKey bool, budget *int) (yaml.Node, error) {
	if *budget--; *budget < 0 {
		return yaml.Node{}, errMonitorsTooLarge
	}
	aliased := false
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n, aliased = n.Alias, true
	}
	out := *n
	out.Anchor = ""
	out.Alias = nil
	if isKey && aliased && out.ShortTag() == "!!merge" {
		out.Tag, out.Style = "!!str", yaml.DoubleQuotedStyle
	}
	if n.Content != nil {
		out.Content = make([]*yaml.Node, len(n.Content))
		for i, child := range n.Content {
			c, err := resolveNode(child, n.Kind == yaml.MappingNode && i%2 == 0, budget)
			if err != nil {
				return yaml.Node{}, err
			}
			out.Content[i] = &c
		}
	}
	return out, nil
}

// presentNode follows aliases and reports whether n carries a value; an
// absent key and an explicit null both mean "use the default".
func presentNode(n *yaml.Node) (*yaml.Node, bool) {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	if n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null") {
		return nil, false
	}
	return n, true
}

// nodeText renders a configured value for a warning: a scalar as written, a
// list or mapping in flow style.
func nodeText(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	flow := *n
	flow.Style = yaml.FlowStyle
	out, err := yaml.Marshal(&flow)
	if err != nil {
		return "<unprintable value>"
	}
	return strings.TrimSpace(string(out))
}

func parseEnabled(n *yaml.Node) (bool, error) {
	var b bool
	if err := n.Decode(&b); err != nil {
		return false, errors.New("is not true or false")
	}
	return b, nil
}

func parsePaths(n *yaml.Node) ([]string, error) {
	var paths []string
	if err := n.Decode(&paths); err != nil {
		return nil, errors.New("is not a list of paths")
	}
	return paths, nil
}

func showDefaultPaths([]string) string { return "the default paths" }

// parseAbsPath reads a single absolute path.
func parseAbsPath(n *yaml.Node) (string, error) {
	if n.Kind != yaml.ScalarNode || !filepath.IsAbs(n.Value) {
		return "", errors.New("is not an absolute path")
	}
	return n.Value, nil
}

func parsePercent(n *yaml.Node) (float64, error) {
	if n.Kind != yaml.ScalarNode {
		return 0, errors.New("is not a number")
	}
	v, err := strconv.ParseFloat(n.Value, 64)
	if err != nil {
		return 0, errors.New("is not a number")
	}
	if !(v >= 0 && v <= 100) {
		return 0, errors.New("is outside 0-100")
	}
	return v, nil
}

func showFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// durationAtLeast parses a time.ParseDuration string no shorter than lowest;
// a negative duration is refused even when lowest is zero.
func durationAtLeast(lowest time.Duration) func(*yaml.Node) (time.Duration, error) {
	return func(n *yaml.Node) (time.Duration, error) {
		if n.Kind != yaml.ScalarNode {
			return 0, errors.New("is not a duration")
		}
		d, err := time.ParseDuration(n.Value)
		switch {
		case err != nil:
			return 0, errors.New("is not a duration")
		case d < 0:
			return 0, errors.New("is negative")
		case d < lowest:
			return 0, fmt.Errorf("is below the %s minimum", lowest)
		}
		return d, nil
	}
}

func parseSizeNode(n *yaml.Node) (uint64, error) {
	if n.Kind != yaml.ScalarNode {
		return 0, errors.New("is not a size")
	}
	return parseSize(n.Value)
}

// sizeUnit is one binary unit a size may carry, as a shift of one byte.
type sizeUnit struct {
	suffix string
	shift  uint
}

// sizeUnits lists the accepted units, largest first so formatSize picks the
// largest one that divides a value exactly.
func sizeUnits() []sizeUnit {
	return []sizeUnit{{"TiB", 40}, {"GiB", 30}, {"MiB", 20}, {"KiB", 10}}
}

// parseSize parses `<int>(KiB|MiB|GiB|TiB)` into bytes. Anything else, a
// negative number, a fraction or a size beyond uint64 is an error.
func parseSize(s string) (uint64, error) {
	for _, u := range sizeUnits() {
		digits, ok := strings.CutSuffix(s, u.suffix)
		if !ok {
			continue
		}
		if strings.HasPrefix(digits, "-") {
			return 0, errors.New("is negative")
		}
		v, err := strconv.ParseUint(digits, 10, 64)
		switch {
		case errors.Is(err, strconv.ErrRange):
			return 0, errors.New("is too large")
		case err != nil:
			return 0, errors.New("is not a whole number of KiB, MiB, GiB or TiB")
		case v > math.MaxUint64>>u.shift:
			return 0, errors.New("is too large")
		}
		return v << u.shift, nil
	}
	return 0, errors.New("is not a size in KiB, MiB, GiB or TiB")
}

// formatSize renders bytes in the largest unit that divides them exactly.
func formatSize(b uint64) string {
	for _, u := range sizeUnits() {
		if b != 0 && b%(1<<u.shift) == 0 {
			return fmt.Sprintf("%d%s", b>>u.shift, u.suffix)
		}
	}
	return fmt.Sprintf("%dB", b)
}
