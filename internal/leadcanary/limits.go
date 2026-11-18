//go:build linux && ataniq_canary

package leadcanary

// The bounds of the unit limits. They are this package's own copies of the six
// bounds that internal/leadunit exports for its Limits, because the two are
// built beside each other; a test in internal/leaddaemon holds that each one
// equals its sibling in leadunit. A config limit outside its bound is refused:
// no limit has a default in code.
const (
	// MinMemoryMax and MaxMemoryMax bound memory_max_bytes, in bytes: 256 MiB
	// to 64 GiB.
	MinMemoryMax uint64 = 256 << 20
	MaxMemoryMax uint64 = 64 << 30
	// MinTasksMax and MaxTasksMax bound tasks_max: 16 to 65,536.
	MinTasksMax uint64 = 16
	MaxTasksMax uint64 = 65536
	// MinSwapMax and MaxSwapMax bound swap_max_bytes, in bytes: 0 (no swap) to
	// 64 GiB.
	MinSwapMax uint64 = 0
	MaxSwapMax uint64 = 64 << 30
)

// The bounds of the memory floor and the share, which the unit does not
// carry: the floor lies between MinMemoryMax and the ceiling, and the share is
// a percentage.
const (
	// MinMemoryFloor is the smallest memory_floor_bytes, the same as
	// MinMemoryMax.
	MinMemoryFloor = MinMemoryMax
	// MinSharePercent and MaxSharePercent bound memory_share_percent.
	MinSharePercent = 1
	MaxSharePercent = 100
)
