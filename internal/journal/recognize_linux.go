package journal

import (
	"context"
	"database/sql/driver"
	"errors"

	"modernc.org/sqlite"
)

// Recognize describes exact released v1 storage for the configured authority
// under exclusive existing-store ownership. It uses bounded read-only SQL and
// never initializes or recovers the journal. Dirty guards and evidence remain
// for ordinary Open recovery. SQLite may create an empty WAL or reconstruct
// its shared-memory index and read marks while observing committed WAL state.
//
// Info has the same meanings as Store.Info, including the configured MaxBytes
// bound. This trusted internal observation grants no migration or disclosure
// authority and retains no ownership after return. Errors return a zero Info,
// including cancellation and SQL or ownership cleanup failures.
func Recognize(ctx context.Context, path string, options Options) (Info, error) {
	return recognizeWithDriver(ctx, path, options, &sqlite.Driver{}, acquire)
}

// The driver and own, which acquires the ownership (acquire in Recognize),
// are per-call test seams, never global registrations.
func recognizeWithDriver(ctx context.Context, path string, options Options, d driver.Driver, own func(path string, create bool) (*ownership, error)) (info Info, err error) {
	o, err := options.normalized()
	if err != nil {
		return Info{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, o.OperationTimeout)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return Info{}, err
	}
	owner, err := own(path, false)
	if err != nil {
		return Info{}, err
	}
	defer func() {
		err = errors.Join(err, owner.close(), ctx.Err())
		if err != nil {
			info = Info{}
		}
	}()
	return recognizeOwned(ctx, owner, o, d)
}

// recognizeOwned requires normalized options, a bounded context and live
// ownership. It closes SQL but never releases the caller's owner, so Open (or
// a future explicitly authorized migration) can continue without a lock gap.
// Recognition itself is never permission to migrate.
func recognizeOwned(ctx context.Context, owner *ownership, o Options, d driver.Driver) (info Info, err error) {
	db := pool(&connector{driver: d, owner: owner, options: o, readOnly: true})
	defer func() {
		// Check the original inode/path after SQL closure and before success.
		err = errors.Join(err, db.Close(), owner.check(), ctx.Err())
		if err != nil {
			info = Info{}
		}
	}()
	return readInfo(ctx, db, o)
}
