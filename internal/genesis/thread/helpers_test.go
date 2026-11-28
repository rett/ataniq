package thread

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/catalog"
)

// clock is a clock the test sets.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *clock { return &clock{at: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// counter is a random source of ids that count up: 0000000000000001, ... in
// each call, and so differ between stores only by where they start.
type counter struct {
	mu sync.Mutex
	n  uint64
}

func (c *counter) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	for i := range p {
		p[i] = 0
	}
	for i, v := 0, c.n; i < 8 && v > 0; i, v = i+1, v>>8 {
		p[7-i] = byte(v)
	}
	return len(p), nil
}

// logBook collects a store's log lines.
type logBook struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBook) logf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, a...))
}

func (l *logBook) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *logBook) count(sub string) int {
	n := 0
	for _, s := range l.all() {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// world is a store in a temporary folder with a fixed clock, ids and log.
type world struct {
	t     *testing.T
	dir   string
	clock *clock
	rand  *counter
	log   *logBook
	s     *Store
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, dir: filepath.Join(t.TempDir(), DirName), clock: newClock(), rand: &counter{}, log: &logBook{}}
	w.open()
	return w
}

// open opens the store, closing a store that is open.
func (w *world) open() {
	w.t.Helper()
	if w.s != nil {
		w.s.Close()
	}
	s, err := Open(w.dir, Options{Clock: w.clock.now, Rand: w.rand, Log: w.log.logf})
	if err != nil {
		w.t.Fatalf("Open: %v", err)
	}
	w.s = s
	w.t.Cleanup(func() { s.Close() })
}

var ctx = context.Background()

// ok fails the test when err is not nil, and checks that the result passes the
// wire's own form, as the gate will encode it.
func (w *world) ok(res ownerwire.SeatGenesisResult, err error) ownerwire.SeatGenesisResult {
	w.t.Helper()
	if err != nil {
		w.t.Fatalf("unexpected error: %v", err)
	}
	if _, err := ownerwire.EncodeSeatGenesisResult(res); err != nil {
		w.t.Fatalf("the result does not pass the wire: %v\n%+v", err, res)
	}
	return res
}

// refused fails the test unless err is the refusal with the code.
func refused(t *testing.T, err error, code string) {
	t.Helper()
	got, ok := RefusalCode(err)
	if !ok || got != code {
		t.Fatalf("error %v, want the refusal %s", err, code)
	}
}

// files lists the names in the store's folder.
func (w *world) files() []string {
	w.t.Helper()
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// bytesOf reads a thread's file.
func (w *world) bytesOf(id string) []byte {
	w.t.Helper()
	b, err := os.ReadFile(filepath.Join(w.dir, fileOf(id)))
	if err != nil {
		w.t.Fatal(err)
	}
	return b
}

func js(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// choice, choices, other, decide and skip are the wire's answers.
func choice(node, id string) ownerwire.GenesisAnswer {
	return ownerwire.GenesisAnswer{Node: node, Kind: ownerwire.GenesisKindChoice, IDs: []string{id}}
}

func choices(node string, ids ...string) ownerwire.GenesisAnswer {
	return ownerwire.GenesisAnswer{Node: node, Kind: ownerwire.GenesisKindChoices, IDs: ids}
}

func other(node, text string) ownerwire.GenesisAnswer {
	return ownerwire.GenesisAnswer{Node: node, Kind: ownerwire.GenesisKindOther, Text: text}
}

func decide(node string) ownerwire.GenesisAnswer {
	return ownerwire.GenesisAnswer{Node: node, Kind: ownerwire.GenesisKindYouDecide}
}

func skip(node string) ownerwire.GenesisAnswer {
	return ownerwire.GenesisAnswer{Node: node, Kind: ownerwire.GenesisKindSkip}
}

// nodesOf lists the node ids of a question result.
func nodesOf(res ownerwire.SeatGenesisResult) []string {
	var out []string
	for _, n := range res.Nodes {
		out = append(out, n.ID)
	}
	return out
}

// webApp is the answers of each round of the web app path: the kind, then the
// rounds of the catalog's own order.
var webApp = [][]ownerwire.GenesisAnswer{
	{choice("kind", "software")},
	{choice("software.sub_kind", "web_app"), choice("goal", "own_use"), choice("audience", "me")},
	{choice("software.stack", "go"), decide("software.data"), choice("software.auth", "passkey")},
	{choice("software.deploy_kind", "local"), choice("hosts", "this_computer"), choice("budget_band", "none")},
	{choices("deliverables", "first_version"), choice("quality_bar", "solid"), skip("schedule")},
	{choices("review_points", "plan", "demo", "publish")},
}

// youDecideWebApp is the web app path with "You decide" on the language, the
// sign-in and where the app runs: the three questions whose answer the host
// infers for the owner (supervisor-inferred), which he does not confirm while
// the interview is open.
var youDecideWebApp = [][]ownerwire.GenesisAnswer{
	{choice("kind", "software")},
	{choice("software.sub_kind", "web_app"), choice("goal", "own_use"), choice("audience", "me")},
	{decide("software.stack"), decide("software.data"), decide("software.auth")},
	{decide("software.deploy_kind"), choice("hosts", "this_computer"), choice("budget_band", "none")},
	{choices("deliverables", "first_version"), choice("quality_bar", "solid"), skip("schedule")},
	{choices("review_points", "plan", "demo", "publish")},
}

// inferredFields are the fields "You decide" infers in youDecideWebApp.
var inferredFields = []string{"software.auth", "software.deploy_kind", "software.stack"}

// answerAll answers the rounds of a path of a kind a template serves in
// order and returns the last result. Each result's progress is in the notion
// plan_ready follows: ready exactly when every required answer is filled.
func (w *world) answerAll(id string, path [][]ownerwire.GenesisAnswer) ownerwire.SeatGenesisResult {
	w.t.Helper()
	var res ownerwire.SeatGenesisResult
	for i, round := range path {
		res = w.ok(w.s.Answer(ctx, id, i+1, round))
		if pr := res.Progress; pr.PlanReady != (pr.Filled == pr.Total) {
			w.t.Fatalf("after round %d: %d of %d filled, plan ready %v", i+1, pr.Filled, pr.Total, pr.PlanReady)
		}
	}
	return res
}

// interview walks the web app path to the summary, checking each round is the
// one the table expects, and returns the summary.
func (w *world) interview(id string, from int) ownerwire.SeatGenesisResult {
	w.t.Helper()
	var res ownerwire.SeatGenesisResult
	for i := from; i < len(webApp); i++ {
		res = w.ok(w.s.Answer(ctx, id, i+1, webApp[i]))
		if i+1 < len(webApp) {
			if res.State != ownerwire.GenesisStateQuestion || res.Round != i+2 {
				w.t.Fatalf("after round %d: state %s round %d", i+1, res.State, res.Round)
			}
		}
	}
	return res
}

func v1(t *testing.T) catalog.Catalog {
	t.Helper()
	c, err := catalog.V1()
	if err != nil {
		t.Fatal(err)
	}
	return c
}
