package seatlink

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/catalog"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// CapGenesis is the capability of seat.genesis, the guided new-project
// interview, in a hello.
const CapGenesis = "genesis"

// The app's copies of the interview's bounds and words are the wire's: each
// difference below is a negative constant, which does not convert to uint,
// if they are not the same number.
const (
	_ = uint(ownerwire.GenesisThreadLength - seatapp.GenesisThreadLength)
	_ = uint(seatapp.GenesisThreadLength - ownerwire.GenesisThreadLength)
	_ = uint(ownerwire.MaxGenesisThreads - seatapp.MaxGenesisThreads)
	_ = uint(seatapp.MaxGenesisThreads - ownerwire.MaxGenesisThreads)
	_ = uint(ownerwire.MaxGenesisRounds - seatapp.MaxGenesisRounds)
	_ = uint(seatapp.MaxGenesisRounds - ownerwire.MaxGenesisRounds)
	_ = uint(ownerwire.MaxGenesisNodes - seatapp.MaxGenesisQuestions)
	_ = uint(seatapp.MaxGenesisQuestions - ownerwire.MaxGenesisNodes)
	_ = uint(ownerwire.MaxGenesisOptions - seatapp.MaxGenesisOptions)
	_ = uint(seatapp.MaxGenesisOptions - ownerwire.MaxGenesisOptions)
	_ = uint(ownerwire.MaxGenesisRows - seatapp.MaxGenesisRows)
	_ = uint(seatapp.MaxGenesisRows - ownerwire.MaxGenesisRows)
	_ = uint(ownerwire.MaxGenesisOther - seatapp.MaxGenesisOther)
	_ = uint(seatapp.MaxGenesisOther - ownerwire.MaxGenesisOther)
	_ = uint(ownerwire.MaxGenesisPrompt - seatapp.MaxGenesisPrompt)
	_ = uint(seatapp.MaxGenesisPrompt - ownerwire.MaxGenesisPrompt)
	_ = uint(ownerwire.MaxGenesisLabel - seatapp.MaxGenesisLabel)
	_ = uint(seatapp.MaxGenesisLabel - ownerwire.MaxGenesisLabel)
	_ = uint(ownerwire.MaxGenesisExample - seatapp.MaxGenesisExample)
	_ = uint(seatapp.MaxGenesisExample - ownerwire.MaxGenesisExample)
	_ = uint(ownerwire.MaxGenesisValue - seatapp.MaxGenesisValue)
	_ = uint(seatapp.MaxGenesisValue - ownerwire.MaxGenesisValue)
)

// How long the link answers a read of the interview from its memory of the
// last answer, without asking the gate (see genesisMemory).
const (
	// listFresh is how long a list is answered from the last one: the
	// card list's reload gap, so that card lists open in several tabs, each
	// of which lists the open projects on every reload, ask the gate at most
	// once in it between them.
	listFresh = 20 * time.Second
	// viewFresh is how long a round of a thread is answered from the
	// link's own last answer for that thread: long enough for the page a
	// form's post redirects to, so that one answer of the owner's is one
	// call of the gate and not two.
	viewFresh = 10 * time.Second
	// maxViews bounds the threads whose last answer the link keeps.
	maxViews = 8
)

// genesisCodes are the codes of the gate seat.genesis maps to the app's
// errors.
var genesisCodes = map[string]error{
	ownerwire.CodeGenesisBusy:          seatapp.ErrGenesisBusy,
	ownerwire.CodeGenesisUnknownThread: seatapp.ErrGenesisUnknownThread,
	ownerwire.CodeGenesisBadAnswer:     seatapp.ErrGenesisBadAnswer,
	ownerwire.CodeGenesisOverCap:       seatapp.ErrGenesisOverCap,
	ownerwire.CodeGenesisSecretShaped:  seatapp.ErrGenesisSecretShaped,
	ownerwire.CodeGenesisDone:          seatapp.ErrGenesisDone,
	ownerwire.CodeGenesisStaleRound:    seatapp.ErrGenesisStaleRound,
	seatgate.CodeRateLimited:           seatapp.ErrBusy,
	seatgate.CodeBusy:                  seatapp.ErrBusy,
	codeTooManyRefusals:                seatapp.ErrBusy,
	codeUnknownOp:                      seatapp.ErrNotOffered,
}

// The errors of the interview the app does not tell apart. Neither holds
// anything of the call or of the answer.
var (
	// errGenesisAnswer is the error of an answer to seat.genesis that is not
	// of the wire's form, or not the answer to the call: a state the verb
	// does not answer, another thread, a text past its cap.
	errGenesisAnswer = errors.New("seatlink: the gate's answer to the interview is not of its form")
	// errGenesisCall is the error of a call that is not one of the verbs
	// with the fields it takes. Nothing is sent.
	errGenesisCall = errors.New("seatlink: the interview's call is not of the wire's form")
)

// The link keeps the interview's calls.
var _ seatapp.Genesis = (*Daemon)(nil)

// v1 is the question catalog this build holds, from which a row of a
// summary is given its question for a change, when the thread is walked
// under it (the digests are equal).
var v1 = sync.OnceValues(catalog.V1)

// Genesis makes one call of the interview (seat.genesis), and only when the
// gate's hello lists "genesis" ([seatapp.ErrNotOffered] otherwise). The call
// is held to the wire's form by its encoder before anything is sent: a
// thread id of another form is [seatapp.ErrGenesisUnknownThread], an answer
// the wire refuses [seatapp.ErrGenesisBadAnswer], and neither is sent. Every
// call that is sent has the room of its kind (kindGenesis); without room it
// is [seatapp.ErrBusy] and is not sent. The answer is read with the wire's
// decoder and must be one the verb answers, of the thread called; it is then
// converted to the app's values with each length checked again. A list, and
// a round asked again soon after, may be answered from the link's memory of
// the last answer (see genesisMemory). Nothing is retried.
func (d *Daemon) Genesis(ctx context.Context, c seatapp.GenesisCall) (seatapp.GenesisView, error) {
	if ctx.Err() != nil {
		return seatapp.GenesisView{}, seatapp.ErrDaemonUnavailable
	}
	if !d.offers(CapGenesis) {
		if _, ok := d.c.Connected(); !ok {
			return seatapp.GenesisView{}, seatapp.ErrDaemonUnavailable
		}
		return seatapp.GenesisView{}, seatapp.ErrNotOffered
	}
	params, err := genesisParams(c)
	if err != nil {
		return seatapp.GenesisView{}, err
	}
	now := d.gnow()
	gen, kept, ok := d.gmem.before(c, now)
	if ok {
		return genesisView(c, kept)
	}
	if !d.budget.take(kindGenesis) {
		return seatapp.GenesisView{}, seatapp.ErrBusy
	}
	var raw json.RawMessage
	if err := d.c.Call(ctx, ownerwire.OpSeatGenesis, json.RawMessage(params), &raw); err != nil {
		mapped := mapCall(ctx, ownerwire.OpSeatGenesis, err, genesisCodes)
		if errors.Is(mapped, seatapp.ErrGenesisUnknownThread) {
			d.gmem.forget(c.Thread)
		}
		return seatapp.GenesisView{}, mapped
	}
	res, err := ownerwire.DecodeSeatGenesisResult(raw)
	if err != nil {
		return seatapp.GenesisView{}, errGenesisAnswer
	}
	v, err := genesisView(c, res)
	if err != nil {
		return seatapp.GenesisView{}, err
	}
	d.gmem.after(c, res, gen, d.gnow())
	return v, nil
}

// needsThread reports whether a verb names a thread.
func needsThread(verb string) bool {
	return verb != seatapp.GenesisVerbStart && verb != seatapp.GenesisVerbList
}

// genesisParams turns the app's call into the wire's params and encodes them
// with the wire's encoder, which holds them to their form and cap.
func genesisParams(c seatapp.GenesisCall) ([]byte, error) {
	p := ownerwire.SeatGenesisParams{Verb: c.Verb, Thread: c.Thread, Round: c.Round, Node: c.Node}
	switch c.Verb {
	case seatapp.GenesisVerbStart, seatapp.GenesisVerbList, seatapp.GenesisVerbRound, seatapp.GenesisVerbSummary,
		seatapp.GenesisVerbFinish, seatapp.GenesisVerbDiscard, seatapp.GenesisVerbAnswer, seatapp.GenesisVerbChange:
	default:
		return nil, errGenesisCall
	}
	if needsThread(c.Verb) && !seatapp.ValidThreadID(c.Thread) {
		return nil, seatapp.ErrGenesisUnknownThread
	}
	answering := c.Verb == seatapp.GenesisVerbAnswer || c.Verb == seatapp.GenesisVerbChange
	switch c.Verb {
	case seatapp.GenesisVerbAnswer:
		for _, a := range c.Answers {
			p.Answers = append(p.Answers, ownerwire.GenesisAnswer{Node: a.Node, Kind: a.Pick.Kind, IDs: ids(a.Pick.IDs), Text: a.Pick.Text})
		}
	case seatapp.GenesisVerbChange:
		p.Answer = &ownerwire.GenesisPick{Kind: c.Pick.Kind, IDs: ids(c.Pick.IDs), Text: c.Pick.Text}
	}
	out, err := ownerwire.EncodeSeatGenesisParams(p)
	if err != nil {
		if answering {
			// The owner's answer is not of the wire's form (an id of
			// another grammar, text past its cap or not plain): it is
			// not sent, and the page says the answer was not taken.
			return nil, seatapp.ErrGenesisBadAnswer
		}
		return nil, errGenesisCall
	}
	return out, nil
}

// ids is a copy of a list of ids, nil when there is none: the wire leaves an
// empty list out.
func ids(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

// answers says which states answer each verb.
var answers = map[string][]string{
	seatapp.GenesisVerbStart:   {ownerwire.GenesisStateQuestion, ownerwire.GenesisStateSummary},
	seatapp.GenesisVerbList:    {ownerwire.GenesisStateThreads},
	seatapp.GenesisVerbRound:   {ownerwire.GenesisStateQuestion, ownerwire.GenesisStateSummary, ownerwire.GenesisStateFinished},
	seatapp.GenesisVerbAnswer:  {ownerwire.GenesisStateQuestion, ownerwire.GenesisStateSummary},
	seatapp.GenesisVerbChange:  {ownerwire.GenesisStateQuestion, ownerwire.GenesisStateSummary},
	seatapp.GenesisVerbSummary: {ownerwire.GenesisStateSummary, ownerwire.GenesisStateFinished},
	seatapp.GenesisVerbFinish:  {ownerwire.GenesisStateFinished},
	seatapp.GenesisVerbDiscard: {ownerwire.GenesisStateDiscarded},
}

// genesisView checks that res answers the call c and turns it into the
// app's values, checking each count and length again with the app's own
// bounds. A result that does not pass is errGenesisAnswer.
func genesisView(c seatapp.GenesisCall, res ownerwire.SeatGenesisResult) (seatapp.GenesisView, error) {
	bad := func() (seatapp.GenesisView, error) { return seatapp.GenesisView{}, errGenesisAnswer }
	ok := false
	for _, s := range answers[c.Verb] {
		ok = ok || res.State == s
	}
	if !ok {
		return bad()
	}
	if c.Verb == seatapp.GenesisVerbList {
		if res.Thread != "" || len(res.Threads) > seatapp.MaxGenesisThreads {
			return bad()
		}
		v := seatapp.GenesisView{State: seatapp.GenesisStateThreads, Threads: []seatapp.GenesisThread{}}
		for _, t := range res.Threads {
			if !seatapp.ValidThreadID(t.Thread) || t.Of < 1 || t.Of > seatapp.MaxGenesisRounds || t.Round < 0 || t.Round > t.Of ||
				t.Filled < 0 || t.Filled > t.Total || t.Total > seatapp.MaxGenesisRows {
				return bad()
			}
			v.Threads = append(v.Threads, seatapp.GenesisThread{Thread: t.Thread, State: t.State, Round: t.Round, Of: t.Of, Filled: t.Filled, Total: t.Total})
		}
		return v, nil
	}
	if !seatapp.ValidThreadID(res.Thread) || (needsThread(c.Verb) && res.Thread != c.Thread) {
		return bad()
	}
	v := seatapp.GenesisView{
		State: res.State, Thread: res.Thread, Round: res.Round, Of: res.Of,
		Filled: res.Progress.Filled, Total: res.Progress.Total, Reset: res.Reset, Digest: res.Digest,
	}
	if v.Of < 0 || v.Of > seatapp.MaxGenesisRounds || v.Round < 0 || v.Round > v.Of || v.Filled < 0 || v.Filled > v.Total ||
		v.Total > seatapp.MaxGenesisRows || v.Reset < 0 || v.Reset > seatapp.MaxGenesisRows || len(res.Nodes) > seatapp.MaxGenesisQuestions {
		return bad()
	}
	for _, n := range res.Nodes {
		q, ok := appQuestion(n)
		if !ok {
			return bad()
		}
		v.Questions = append(v.Questions, q)
	}
	if res.State == ownerwire.GenesisStateQuestion && len(v.Questions) == 0 {
		return bad()
	}
	if g := res.Groups; g != nil {
		if len(g.YouChose)+len(g.DecidedForYou)+len(g.Defaults) > seatapp.MaxGenesisRows {
			return bad()
		}
		var err error
		if v.YouChose, err = appRows(g.YouChose, res.CatalogDigest); err != nil {
			return bad()
		}
		if v.DecidedForYou, err = appRows(g.DecidedForYou, res.CatalogDigest); err != nil {
			return bad()
		}
		if v.Defaults, err = appRows(g.Defaults, res.CatalogDigest); err != nil {
			return bad()
		}
	}
	return v, nil
}

// text reports whether s is 1 to max bytes of valid UTF-8.
func text(s string, max int) bool { return s != "" && len(s) <= max && utf8.ValidString(s) }

// appQuestion turns a question of the wire into the app's, and reports false
// for one whose counts or texts are past the app's bounds.
func appQuestion(n ownerwire.GenesisNode) (seatapp.GenesisQuestion, bool) {
	q := seatapp.GenesisQuestion{ID: n.ID, Type: n.Type, Prompt: n.Prompt, Topic: n.Topic,
		Other: n.AllowOther, YouDecide: n.AllowYouDecide, Skip: n.CanSkip}
	switch n.Type {
	case seatapp.GenesisTypeSingle, seatapp.GenesisTypeMulti, seatapp.GenesisTypeYesNo, seatapp.GenesisTypeScale:
	default:
		return q, false
	}
	if !text(n.ID, ownerwire.MaxGenesisNodeBytes) || !text(n.Prompt, seatapp.MaxGenesisPrompt) || len(n.Options) > seatapp.MaxGenesisOptions ||
		(n.Type == seatapp.GenesisTypeScale) != (len(n.Options) == 0) {
		return q, false
	}
	for _, o := range n.Options {
		if !text(o.ID, ownerwire.MaxGenesisChoiceBytes) || !text(o.Label, seatapp.MaxGenesisLabel) || !text(o.Example, seatapp.MaxGenesisExample) {
			return q, false
		}
		q.Options = append(q.Options, seatapp.GenesisOption{ID: o.ID, Label: o.Label, Example: o.Example})
	}
	return q, true
}

// appRows turns one group of a summary into the app's rows. A row is given
// its question from the catalog this build holds when the thread is walked
// under it, so the seat can offer a change of the answer; under another
// catalog, it has none.
func appRows(rows []ownerwire.GenesisRow, catalogDigest string) ([]seatapp.GenesisRow, error) {
	cat, err := v1()
	known := err == nil && catalogDigest != "" && cat.Digest() == catalogDigest
	out := []seatapp.GenesisRow{}
	for _, r := range rows {
		max := seatapp.MaxGenesisValue
		if r.Untrusted {
			max = seatapp.MaxGenesisOther
		}
		if !text(r.Node, ownerwire.MaxGenesisNodeBytes) || !text(r.Label, seatapp.MaxGenesisPrompt) || !text(r.Value, max) {
			return nil, errGenesisAnswer
		}
		row := seatapp.GenesisRow{Node: r.Node, Field: r.Field, Label: r.Label, Value: r.Value, OwnersWords: r.Untrusted}
		if known {
			if n, ok := cat.Node(r.Node); ok && n.Field == r.Field {
				if q, ok := appQuestion(wireNode(n)); ok {
					row.Question = &q
				}
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// wireNode is a question of the catalog as the wire words it, as the thread
// store makes it: the catalog's texts, and a skip when the node has a
// default.
func wireNode(n catalog.Node) ownerwire.GenesisNode {
	out := ownerwire.GenesisNode{
		ID: n.ID, Type: string(n.Type), Prompt: n.Prompt, Topic: n.Topic, Options: []ownerwire.GenesisOption{},
		AllowOther: n.AllowOther, AllowYouDecide: n.AllowYouDecide, CanSkip: n.Default != "",
	}
	for _, o := range n.Options {
		out.Options = append(out.Options, ownerwire.GenesisOption{ID: o.ID, Label: o.Label, Example: o.Example})
	}
	return out
}

// genesisMemory is the link's short memory of the interview's answers, so
// that a read the pages make again and again does not spend the room of the
// owner's own taps: the last list, for listFresh, and the last answer for
// each thread (its round as it stands), for viewFresh. The link is the seat's
// one way to the threads, and every call that changes one goes through it,
// so what it remembers is what the gate would answer, but for a thread that
// expired meanwhile (a page then meets genesis-unknown-thread at its next
// post). A call that changes threads (start, answer, change, finish,
// discard) forgets the list and its thread before it is sent, whatever its
// answer, and an answer is kept only when no such call began while it was
// asked (gen), so an answer that crossed a change is never kept. Nothing is
// kept past maxViews threads, and what is stale is dropped at each call.
type genesisMemory struct {
	mu    sync.Mutex
	gen   uint64
	list  *kept
	views map[string]kept
}

// kept is one answer the link remembers, and when it was given.
type kept struct {
	res ownerwire.SeatGenesisResult
	at  time.Time
}

// changes reports whether a verb changes threads.
func changes(verb string) bool {
	switch verb {
	case seatapp.GenesisVerbStart, seatapp.GenesisVerbAnswer, seatapp.GenesisVerbChange, seatapp.GenesisVerbFinish, seatapp.GenesisVerbDiscard:
		return true
	}
	return false
}

// before runs before a call is sent. A list or a round that the memory
// answers is returned with ok; otherwise it returns the generation the
// answer is to be kept under, having forgotten, for a call that changes
// threads, the list and the call's thread.
func (m *genesisMemory) before(c seatapp.GenesisCall, now time.Time) (gen uint64, res ownerwire.SeatGenesisResult, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(now)
	switch {
	case c.Verb == seatapp.GenesisVerbList && m.list != nil:
		return m.gen, m.list.res, true
	case c.Verb == seatapp.GenesisVerbRound:
		if k, found := m.views[c.Thread]; found {
			return m.gen, k.res, true
		}
	case changes(c.Verb):
		m.gen++
		m.list = nil
		delete(m.views, c.Thread)
	}
	return m.gen, ownerwire.SeatGenesisResult{}, false
}

// after keeps the answer to a call, when no call that changes threads began
// since gen: a list as the list, and any other answer that is where its
// thread stands now (a round, or the answer to a call that changed it) as
// that thread's round, with no reset. A discard is kept as nothing.
func (m *genesisMemory) after(c seatapp.GenesisCall, res ownerwire.SeatGenesisResult, gen uint64, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if gen != m.gen {
		return
	}
	switch c.Verb {
	case seatapp.GenesisVerbList:
		m.list = &kept{res: res, at: now}
	case seatapp.GenesisVerbRound, seatapp.GenesisVerbStart, seatapp.GenesisVerbAnswer, seatapp.GenesisVerbChange, seatapp.GenesisVerbFinish:
		if m.views == nil || len(m.views) >= maxViews {
			m.views = map[string]kept{}
		}
		res.Reset = 0
		m.views[res.Thread] = kept{res: res, at: now}
	}
}

// forget drops what the memory holds of a thread, and the list.
func (m *genesisMemory) forget(thread string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gen++
	m.list = nil
	delete(m.views, thread)
}

// prune drops what is stale. It runs under mu.
func (m *genesisMemory) prune(now time.Time) {
	if m.list != nil && (now.Sub(m.list.at) >= listFresh || now.Before(m.list.at)) {
		m.list = nil
	}
	for t, k := range m.views {
		if now.Sub(k.at) >= viewFresh || now.Before(k.at) {
			delete(m.views, t)
		}
	}
}
