package ownerwire

import (
	"strconv"
	"time"

	"ataniq/internal/approval/record"
)

// This file holds the checks of a payload's content that the decoders and
// the encoders share. What the codec already holds (the types, the keys,
// the control characters) is not repeated here. No detail written here
// holds a value the payload brought.

// checker collects the first refusal of a payload. invalid is the code for
// a broken form.
type checker struct {
	invalid record.Code
	err     *record.Error
}

// fail records a refusal with its own code, unless one is recorded already.
func (c *checker) fail(code record.Code, detail string) {
	if c.err == nil {
		c.err = &record.Error{Code: code, Detail: detail}
	}
}

// need records a refusal with the form's code when ok is false.
func (c *checker) need(ok bool, detail string) {
	if !ok {
		c.fail(c.invalid, detail)
	}
}

// result returns the first refusal as an error, or nil. It returns the
// untyped nil, so that a caller's "err != nil" is right.
func (c *checker) result() error {
	if c.err == nil {
		return nil
	}
	return c.err
}

func (c *checker) pendingID(s, where string) {
	c.need(record.ValidPendingID(s), where+" is not ap- and 1 to 48 lower-case letters, digits and dashes")
}

func (c *checker) sha256(s, where string) {
	c.need(record.IsLowerHex(s, 64), where+" is not 64 lower-case hexadecimal digits")
}

func (c *checker) time(s, where string) {
	_, err := record.ParseTime(s)
	c.need(err == nil, where+" is not a time in UTC to the second, ending in Z")
}

func (c *checker) filled(s, where string) {
	c.need(s != "", where+" is empty")
}

func (c *checker) count(n int64, where string) {
	c.need(n >= 0, where+" is negative")
}

// hostLine checks a line the host wrote. The codec holds that it has no
// control character; its length is held here.
func (c *checker) hostLine(s, where string) {
	c.need(len(s) <= MaxHostLineBytes, where+" is larger than "+strconv.Itoa(MaxHostLineBytes)+" bytes")
}

// name checks a short name: not empty, and at most [MaxNameBytes].
func (c *checker) name(s, where string) {
	c.need(s != "" && len(s) <= MaxNameBytes, where+" is not 1 to "+strconv.Itoa(MaxNameBytes)+" bytes")
}

func (c *checker) state(s record.State, where string) {
	c.need(s.Valid(), where+" is not a state of a pending record")
}

// verb checks a verb against the closed list, and the feature name that
// goes with it: a feature finish names one, a prune names none.
func (c *checker) verb(verb, feature, where string) {
	switch verb {
	case record.VerbFeatureFinish:
		c.need(record.ValidFeatureName(feature), where+".feature is not a feature name of 1 to 48 lower-case letters, digits and dashes")
	case record.VerbBranchPrune:
		c.need(feature == "", where+".feature is given for a prune")
	default:
		c.need(false, where+".verb is not a verb that can be asked for")
	}
}

// carried checks a record that travels as bytes: it is there, it is within
// the cap of a pending record, and its digest is the bytes' own. A digest
// that is another's is refused binding-mismatch.
func (c *checker) carried(r *Record, where string) {
	c.need(len(r.Bytes) > 0, where+".bytes is empty")
	if len(r.Bytes) > record.MaxPendingBytes {
		c.fail(record.CodeRecordTooLarge, where+".bytes is larger than "+strconv.Itoa(record.MaxPendingBytes)+" bytes")
	}
	c.sha256(r.SHA256, where+".sha256")
	if c.err == nil && record.Digest(r.Bytes) != r.SHA256 {
		c.fail(record.CodeBindingMismatch, where+".sha256 is not the digest of "+where+".bytes")
	}
}

// page checks what the two kinds of page share: whose page it is, its
// number and the number of pages. empty is whether the page holds nothing.
func (c *checker) page(id, digest string, page, pages int, empty bool, where string) {
	c.pendingID(id, where+".id")
	c.sha256(digest, where+".review_digest")
	c.need(pages >= 1 && pages <= MaxPages, where+".pages is not 1 to "+strconv.Itoa(MaxPages))
	c.need(page >= 0 && page < pages, where+".page is not a page of .pages, counted from 0")
	c.need(!empty || pages == 1, where+" holds nothing and is not the one page of an empty list")
}

// validDate reports whether s is a calendar day as YYYY-MM-DD.
func validDate(s string) bool {
	const layout = "2006-01-02"
	t, err := time.Parse(layout, s)
	return err == nil && t.Format(layout) == s
}

func checkListing(l *Listing) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	seen := make(map[string]struct{}, len(l.Pending))
	for i := range l.Pending {
		p, at := &l.Pending[i], "listing.pending["+strconv.Itoa(i)+"]"
		c.pendingID(p.ID, at+".id")
		_, dup := seen[p.ID]
		c.need(!dup, at+" repeats an earlier id")
		seen[p.ID] = struct{}{}
		c.sha256(p.SHA256, at+".sha256")
		if p.BindingSHA256 != "" {
			c.sha256(p.BindingSHA256, at+".binding_sha256")
		}
		c.filled(p.Project, at+".project")
		c.verb(p.Verb, p.Feature, at)
		c.time(p.RequestedAt, at+".requested_at")
		c.time(p.PendingExpiresAt, at+".pending_expires_at")
	}
	c.need(len(l.Recent) <= MaxRecent, "listing.recent has more than "+strconv.Itoa(MaxRecent)+" rows")
	for i := range l.Recent {
		r, at := &l.Recent[i], "listing.recent["+strconv.Itoa(i)+"]"
		c.pendingID(r.ID, at+".id")
		c.filled(r.Project, at+".project")
		c.verb(r.Verb, r.Feature, at)
		c.state(r.State, at+".state")
		c.hostLine(r.Line, at+".line")
		c.time(r.At, at+".at")
	}
	for i := range l.Rules {
		r, at := &l.Rules[i], "listing.rules["+strconv.Itoa(i)+"]"
		c.name(r.ID, at+".id")
		c.filled(r.Project, at+".project")
		c.hostLine(r.Line, at+".line")
		c.count(r.Uses, at+".uses")
	}
	c.count(l.NotListed, "listing.not_listed")
	c.count(l.Count.Requests, "listing.count.requests")
	c.count(l.Count.WouldMeet, "listing.count.would_meet")
	c.need(l.Count.WouldMeet <= l.Count.Requests, "listing.count.would_meet is more than listing.count.requests")
	if l.LookedAt != "" {
		c.time(l.LookedAt, "listing.looked_at")
	}
	return c.result()
}

func checkShowParams(p *ShowParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.pendingID(p.ID, "show.id")
	return c.result()
}

func checkShowResult(r *ShowResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.carried(&r.Record, "shown.record")
	if r.Outcome != nil {
		c.state(r.Outcome.State, "shown.outcome.state")
		c.hostLine(r.Outcome.Line, "shown.outcome.line")
	}
	if r.Earlier != nil {
		c.carried(&r.Earlier.Record, "shown.earlier.record")
		c.time(r.Earlier.ReadAt, "shown.earlier.read_at")
	}
	return c.result()
}

func checkPageParams(p *PageParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.pendingID(p.ID, "page.id")
	c.sha256(p.ReviewDigest, "page.review_digest")
	c.need(p.Page >= 0 && p.Page < MaxPages, "page.page is not 0 to "+strconv.Itoa(MaxPages-1))
	return c.result()
}

func checkPathsPage(p *PathsPage) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.page(p.ID, p.ReviewDigest, p.Page, p.Pages, len(p.Paths) == 0, "paths")
	for i := range p.Paths {
		e, at := &p.Paths[i], "paths.paths["+strconv.Itoa(i)+"]"
		c.need(e.Status == "A" || e.Status == "M" || e.Status == "D" || e.Status == "T", at+".status is not A, M, D or T")
		c.need(record.ValidMode(e.OldMode), at+".old_mode is not six octal digits")
		c.need(record.ValidMode(e.NewMode), at+".new_mode is not six octal digits")
		c.count(e.Added, at+".added")
		c.count(e.Removed, at+".removed")
		c.count(e.NewBytes, at+".new_bytes")
	}
	return c.result()
}

// checkPatchPage holds the rules the console's pager ends a reading on, as
// far as one page shows them: no empty piece that continues, no line in
// more than [MaxLinePieces] pieces, and a last page that ends its last
// line.
func checkPatchPage(p *PatchPage) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.page(p.ID, p.ReviewDigest, p.Page, p.Pages, len(p.Lines) == 0, "patch")
	run := 0
	for i := range p.Lines {
		l, at := &p.Lines[i], "patch.lines["+strconv.Itoa(i)+"]"
		if !l.Cont {
			run = 0
			continue
		}
		run++
		c.need(l.Text != "", at+" is an empty piece that continues")
		c.need(run < MaxLinePieces, at+" is piece "+strconv.Itoa(MaxLinePieces)+" of a line and continues")
	}
	if n := len(p.Lines); n > 0 && p.Page == p.Pages-1 {
		c.need(!p.Lines[n-1].Cont, "patch.lines["+strconv.Itoa(n-1)+"] continues at the end of the last page")
	}
	return c.result()
}

func checkDeciderParams(p *DeciderParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.name(p.Client, "decider.client")
	c.need(record.IsLowerHex(p.Session, 16), "decider.session is not 16 lower-case hexadecimal digits")
	return c.result()
}

func checkDeciderResult(r *DeciderResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.need(!r.Decider || r.Reason == "", "decider.reason is given to the decider")
	c.need(r.Decider || r.Reason != "", "decider.reason is missing; a connection that is not the decider is told why")
	c.hostLine(r.Reason, "decider.reason")
	return c.result()
}

func checkDecisionFrame(f *DecisionFrame) error {
	c := &checker{invalid: record.CodeDecisionInvalid}
	c.pendingID(f.ID, "decision.id")
	c.sha256(f.BindingSHA256, "decision.binding_sha256")
	seen := make(map[record.SignOff]struct{}, len(f.SignedOff))
	for i, s := range f.SignedOff {
		at := "decision.signed_off[" + strconv.Itoa(i) + "]"
		c.need(record.ValidFlagKind(s.Kind), at+".kind is not a kind of flag")
		_, dup := seen[s]
		c.need(!dup, at+" repeats an earlier sign-off")
		seen[s] = struct{}{}
	}
	switch f.Decision {
	case record.DecisionApprove:
		c.need(f.Reason == "", "decision.reason is given for an approve")
	case record.DecisionDeny:
		c.need(len(f.SignedOff) == 0, "decision.signed_off is not empty for a deny")
		c.need(len(f.Reason) <= record.MaxReasonBytes, "decision.reason is larger than "+strconv.Itoa(record.MaxReasonBytes)+" bytes")
	default:
		c.need(false, "decision.decision is not approve or deny")
	}
	return c.result()
}

func checkDecideResult(r *DecideResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.state(r.State, "outcome.state")
	c.hostLine(r.Line, "outcome.line")
	c.need(!r.Held || r.State == record.StatePending, "outcome.held is true for a record that is not pending")
	return c.result()
}

func checkLimitParams(p *LimitParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.filled(p.Project, "limit.project")
	return c.result()
}

func checkLimitSetParams(p *LimitSetParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.filled(p.Project, "limit.project")
	c.need(p.To >= 1, "limit.to is under 1")
	return c.result()
}

func checkLimitView(v *LimitView) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.filled(v.Project, "limit.project")
	c.need(v.Limit >= 1, "limit.limit is under 1")
	c.need(len(v.Days) <= MaxLimitDays, "limit.days has more than "+strconv.Itoa(MaxLimitDays)+" days")
	for i := range v.Days {
		d, at := &v.Days[i], "limit.days["+strconv.Itoa(i)+"]"
		c.need(validDate(d.Date), at+".date is not a day as YYYY-MM-DD")
		c.need(d.Uses >= 0, at+".uses is negative")
	}
	c.need(v.Suggestion >= 0, "limit.suggestion is negative")
	return c.result()
}

// validPairCode reports whether s has the form of a pairing code:
// [PairCodeLength] characters of Crockford's base32 without I, L, O and U, as
// package seatpair has it, in either case.
func validPairCode(s string) bool {
	if len(s) != PairCodeLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
			switch c | 0x20 {
			case 'i', 'l', 'o', 'u':
				return false
			}
		default:
			return false
		}
	}
	return true
}

// words checks a fingerprint: [PairWords] words of 3 to 6 lower-case letters.
func (c *checker) words(w []string, where string) {
	c.need(len(w) == PairWords, where+" is not "+strconv.Itoa(PairWords)+" words")
	for i, s := range w {
		ok := len(s) >= 3 && len(s) <= 6
		for j := 0; ok && j < len(s); j++ {
			ok = s[j] >= 'a' && s[j] <= 'z'
		}
		c.need(ok, where+"["+strconv.Itoa(i)+"] is not 3 to 6 lower-case letters")
	}
}

// word checks a short word: 1 to 32 lower-case letters, digits and dashes,
// starting with a letter.
func (c *checker) word(s, where string) {
	ok := len(s) >= 1 && len(s) <= 32
	for i := 0; ok && i < len(s); i++ {
		switch ch := s[i]; {
		case ch >= 'a' && ch <= 'z':
		case i > 0 && (ch >= '0' && ch <= '9' || ch == '-'):
		default:
			ok = false
		}
	}
	c.need(ok, where+" is not 1 to 32 lower-case letters, digits and dashes, starting with a letter")
}

func checkPairStartParams(*PairStartParams) error { return nil }

func checkPairStartResult(r *PairStartResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.need(validPairCode(r.Code), "pair.code is not a pairing code")
	c.time(r.ExpiresAt, "pair.expires_at")
	return c.result()
}

func checkPairStatus(s *PairStatus) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	switch s.Phase {
	case PairNone:
		c.need(s.ExpiresAt == "", "pair.expires_at is given for no pairing")
	case PairWaiting, PairExpired:
		c.time(s.ExpiresAt, "pair.expires_at")
	case PairPending:
		c.time(s.ExpiresAt, "pair.expires_at")
	default:
		c.need(false, "pair.phase is not none, waiting, pending or expired")
	}
	if s.Phase == PairPending {
		c.words(s.Words, "pair.words")
		c.need(record.IsLowerHex(s.PendingID, PendingIDLength), "pair.pending_id is not "+strconv.Itoa(PendingIDLength)+" lower-case hexadecimal digits")
		c.claim(s.Kind, s.AAGUID, s.BackupEligible, s.BackupState, "pair")
	} else {
		c.need(s.Words == nil, "pair.words is given for a pairing that is not pending")
		c.need(s.PendingID == "", "pair.pending_id is given for a pairing that is not pending")
		c.need(s.Kind == "" && s.AAGUID == "" && !s.BackupEligible && !s.BackupState, "pair.kind, pair.aaguid or a backup flag is given for a pairing that is not pending")
	}
	return c.result()
}

// claim checks what an authenticator claimed: a kind when anything else is
// given, an AAGUID of its form, the backup state only with eligibility.
func (c *checker) claim(kind, aaguid string, be, bs bool, where string) {
	if kind != "" {
		c.word(kind, where+".kind")
	}
	c.need(kind != "" || (aaguid == "" && !be && !bs), where+".aaguid or a backup flag is given with no kind")
	c.aaguid(aaguid, where+".aaguid")
	c.need(!bs || be, where+".backup_state is given without backup_eligible")
}

// aaguid checks an AAGUID: absent, or the lower-case 8-4-4-4-12 form of one
// that is not all zero (an all-zero AAGUID is left out).
func (c *checker) aaguid(s, where string) {
	if s == "" {
		return
	}
	ok := len(s) == 36 && s != "00000000-0000-0000-0000-000000000000"
	for i := 0; ok && i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			ok = s[i] == '-'
		default:
			ok = record.IsLowerHex(s[i:i+1], 1)
		}
	}
	c.need(ok, where+" is not an AAGUID in the lower-case 8-4-4-4-12 form")
}

// validRPID reports whether s can be a relying party id: 1 to [MaxRPIDBytes]
// lower-case letters, digits, dots and dashes.
func validRPID(s string) bool {
	if len(s) < 1 || len(s) > MaxRPIDBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '.', ch == '-':
		default:
			return false
		}
	}
	return true
}

func checkPairPendingParams(p *PairPendingParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.need(record.IsLowerHex(p.PendingID, PendingIDLength), "pair.pending_id is not "+strconv.Itoa(PendingIDLength)+" lower-case hexadecimal digits")
	return c.result()
}

func checkSeatPairParams(p *SeatPairParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.need(validPairCode(p.Code), "seat.code is not a pairing code")
	c.need(len(p.CredentialID) >= MinCredentialIDBytes && len(p.CredentialID) <= MaxCredentialIDBytes,
		"seat.credential_id is not "+strconv.Itoa(MinCredentialIDBytes)+" to "+strconv.Itoa(MaxCredentialIDBytes)+" bytes")
	c.need(len(p.PublicKey) >= 1 && len(p.PublicKey) <= MaxPublicKeyBytes,
		"seat.public_key is not 1 to "+strconv.Itoa(MaxPublicKeyBytes)+" bytes")
	if p.AuthenticatorData == nil && p.RPID == "" {
		return c.result()
	}
	c.need(len(p.AuthenticatorData) >= MinRegistrationDataBytes && len(p.AuthenticatorData) <= MaxRegistrationDataBytes,
		"seat.authenticator_data is not "+strconv.Itoa(MinRegistrationDataBytes)+" to "+strconv.Itoa(MaxRegistrationDataBytes)+" bytes")
	c.need(validRPID(p.RPID), "seat.rp_id is not 1 to "+strconv.Itoa(MaxRPIDBytes)+" lower-case letters, digits, dots and dashes")
	return c.result()
}

func checkSeatPairResult(r *SeatPairResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.words(r.Words, "seat.words")
	return c.result()
}

func (c *checker) keyItem(k *KeyItem, where string) {
	c.need(record.IsLowerHex(k.ID, KeyIDLength), where+".id is not "+strconv.Itoa(KeyIDLength)+" lower-case hexadecimal digits")
	c.word(k.Kind, where+".kind")
	c.time(k.PairedAt, where+".paired_at")
	c.aaguid(k.AAGUID, where+".aaguid")
	c.need(!k.BackupState || k.BackupEligible, where+".backup_state is given without backup_eligible")
}

func checkKeyItem(k *KeyItem) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.keyItem(k, "key")
	return c.result()
}

func checkKeysView(v *KeysView) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.need(len(v.Keys) <= MaxKeys, "keys.keys has more than "+strconv.Itoa(MaxKeys)+" keys")
	seen := make(map[string]struct{}, len(v.Keys))
	for i := range v.Keys {
		at := "keys.keys[" + strconv.Itoa(i) + "]"
		c.keyItem(&v.Keys[i], at)
		_, dup := seen[v.Keys[i].ID]
		c.need(!dup, at+" repeats an earlier id")
		seen[v.Keys[i].ID] = struct{}{}
	}
	return c.result()
}

func checkKeyRevokeParams(p *KeyRevokeParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	ok := len(p.ID) >= MinKeyIDPrefix && len(p.ID) <= KeyIDLength && record.IsLowerHex(p.ID, len(p.ID))
	c.need(ok, "key.id is not "+strconv.Itoa(MinKeyIDPrefix)+" to "+strconv.Itoa(KeyIDLength)+" lower-case hexadecimal digits")
	return c.result()
}
