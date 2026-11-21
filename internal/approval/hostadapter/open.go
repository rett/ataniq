//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/intake"
	"ataniq/internal/leaddaemon"
)

// The names inside the owner folder of the daemon's state folder.
const (
	// OwnerDirName is the folder of the owner channel inside the state
	// folder. It is private to the user.
	OwnerDirName = "owner"
	// JournalName is the journal's file inside it.
	JournalName = "journal.db"
	// RecordsName is the intake's records folder inside it: the
	// verification records and the intake records.
	RecordsName = "records"
)

// Default periods of the loops of an [Opened].
const (
	DefaultScanEvery = 2 * time.Second
	// DefaultTickEvery is how often the service is ticked. It is far inside
	// the 30 minutes an approval lasts, as the service asks.
	DefaultTickEvery = 30 * time.Second
)

// OpenConfig is what [Open] makes the owner channel's service from.
type OpenConfig struct {
	Config
	// StateDir is the daemon's state folder, a clean absolute path. The
	// journal and the records are in OwnerDirName below it, which is made
	// private when it is not there.
	StateDir string
	// Door is the planner's door. The scratch pass gives the stand-in.
	Door door.Door
	// Ender is optional; see [ownerchannel.RequestEnder].
	Ender ownerchannel.RequestEnder
	// DropDir is the drop folder of the collector, a private folder that
	// exists. Empty means no collector: the service is built and nothing
	// collects.
	DropDir string
	// CollectProject is the project the collector names for each request.
	// It is required when DropDir is set.
	CollectProject string
	// Manual says that no loop is started: the caller scans the collector
	// and ticks the service itself. A test uses it to run each step when it
	// wants it.
	Manual bool
	// ScanEvery and TickEvery are the periods of the collector's scan and
	// of the service's tick; zero values take the defaults.
	ScanEvery, TickEvery time.Duration
	// Service holds the settings of the service that the adapter does not
	// own (the presence window, the approval's life, the fault hook of a
	// test). Its journal, door, merger, intake, verifier, mailbox, clock
	// and notice are set by Open.
	Service ownerchannel.Config
}

// Opened is the owner channel's service with everything it stands on.
type Opened struct {
	// Service is the host's side of a card. As the daemon needs it, it is a
	// [leaddaemon.OwnerService].
	Service *ownerchannel.Service
	// Host holds the adapters.
	Host *Host
	// Collector is the file-drop collector, or nil without a drop folder.
	Collector *Collector

	store  *intake.Store
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
	err    error
}

// Open makes the owner channel's service on the real journal in the state
// folder, the real merge, intake and records, and the file mailbox, and starts
// the collector's loops when there is a drop folder. The service's patch is
// read through [leaddaemon.UnescapingIntake], which turns the intake's escaped
// lines back into the raw ones the service pages. Close ends everything.
func Open(ctx context.Context, cfg OpenConfig) (*Opened, error) {
	if cfg.StateDir == "" || !filepath.IsAbs(cfg.StateDir) || filepath.Clean(cfg.StateDir) != cfg.StateDir {
		return nil, errors.New("hostadapter: the state folder is not a clean absolute path")
	}
	if cfg.Door == nil {
		return nil, errors.New("hostadapter: no door")
	}
	if cfg.ScanEvery < 0 || cfg.TickEvery < 0 {
		return nil, errors.New("hostadapter: a period is negative")
	}
	if cfg.DropDir != "" && cfg.CollectProject == "" {
		return nil, errors.New("hostadapter: a drop folder needs the project its requests are for")
	}
	owner := filepath.Join(cfg.StateDir, OwnerDirName)
	records := filepath.Join(owner, RecordsName)
	for _, d := range []string{owner, records} {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if err := checkPrivateDir(d); err != nil {
			return nil, err
		}
	}
	store, err := intake.OpenStore(records)
	if err != nil {
		return nil, err
	}
	fail := func(err error, more ...func()) (*Opened, error) {
		for _, f := range more {
			f()
		}
		_ = store.Close()
		return nil, err
	}
	dropDir := cfg.DropDir
	if dropDir == "" {
		// A mailbox is needed whether or not anything collects; with no
		// drop folder the replies go to a folder of their own.
		dropDir = filepath.Join(owner, "replies")
		if err := os.Mkdir(dropDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fail(err)
		}
	}
	mail, err := NewFileMailbox(dropDir)
	if err != nil {
		return fail(err)
	}
	host, err := New(cfg.Config, store, mail)
	if err != nil {
		return fail(err)
	}
	jpath := filepath.Join(owner, JournalName)
	journal, err := ownerjournal.Open(jpath)
	if err != nil {
		return fail(fmt.Errorf("hostadapter: the journal: %w", err))
	}
	sc := cfg.Service
	sc.Journal = journal
	sc.Reopen = func() (ownerchannel.Journal, error) { return ownerjournal.Open(jpath) }
	sc.Door, sc.Ender = cfg.Door, cfg.Ender
	sc.Merger, sc.Verifier, sc.Mailbox = host, host, mail
	sc.Intake = leaddaemon.UnescapingIntake{Intake: host}
	sc.Now = cfg.now
	sc.Notice = cfg.Notice
	svc, err := ownerchannel.New(ctx, sc)
	if err != nil {
		_ = journal.Close()
		return fail(err)
	}
	o := &Opened{Service: svc, Host: host, store: store}
	loopCtx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	if cfg.DropDir != "" {
		col, err := NewCollector(mail, cfg.CollectProject, svc.Submit, cfg.now, cfg.Notice)
		if err != nil {
			cancel()
			_ = svc.Close()
			return fail(err)
		}
		col.known = func(ctx context.Context, id string) (bool, error) {
			if _, ok := svc.Status(id); ok {
				return true, nil
			}
			list, err := cfg.Door.List(ctx)
			if err != nil {
				return false, err
			}
			for _, e := range list {
				if e.RequestID == id {
					return true, nil
				}
			}
			return false, nil
		}
		o.Collector = col
		scan, tick := cfg.ScanEvery, cfg.TickEvery
		if scan == 0 {
			scan = DefaultScanEvery
		}
		if tick == 0 {
			tick = DefaultTickEvery
		}
		if !cfg.Manual {
			o.loop(loopCtx, scan, func(ctx context.Context) { _, _ = col.Scan(ctx) })
			o.loop(loopCtx, tick, func(ctx context.Context) {
				if err := svc.Tick(ctx); err != nil && ctx.Err() == nil {
					cfg.notice("the service's tick: %s", oneLine(err.Error()))
				}
			})
		}
	}
	return o, nil
}

// loop runs fn now and then every period, until ctx ends.
func (o *Opened) loop(ctx context.Context, every time.Duration, fn func(context.Context)) {
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			fn(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Close stops the loops, closes the service, which closes the journal, and
// closes the records folder. It returns the first error and may be called
// more than once.
func (o *Opened) Close() error {
	o.once.Do(func() {
		o.cancel()
		o.wg.Wait()
		o.err = errors.Join(o.Service.Close(), o.store.Close())
	})
	return o.err
}
