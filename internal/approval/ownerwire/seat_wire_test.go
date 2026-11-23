package ownerwire

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

// The samples of the seat gate's forms. The instance, the nonces, the id and
// the binding digest are those of the test vectors of docs/design/seat-gate.md.
const (
	vectorInstance = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	vectorBinding  = "12969c837bce0f6598da9ece4c5e885ec6a669e731f542dbb4a05d3313aabf6c"
	sampleChallID  = "0011223344556677"
	sampleKeyID    = "be45cb2605bf36be" // the display id of the bytes 0x00 to 0x0f
)

func byteRun(from, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(from + i)
	}
	return b
}

// vectorSignOffs are the two sign-offs of the record package's test record:
// the second path holds an escape byte, so the record writes it as b64.
func vectorSignOffs() []record.SignOff {
	return []record.SignOff{
		{Kind: record.FlagExecConfig, Path: "Makefile"},
		{Kind: record.FlagSymlink, Path: "tools/\x1b[2Jclear"},
	}
}

func sampleAssertion() Assertion {
	return Assertion{
		CredentialID:      byteRun(0, 16),
		AuthenticatorData: bytes.Repeat([]byte{0xaa}, 37),
		ClientDataJSON:    []byte(`{"type":"webauthn.get"}`),
		Signature:         []byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01},
	}
}

func sampleSeatChallengeParams() SeatChallengeParams {
	return SeatChallengeParams{Frame: sampleFrame(), Credential: sampleKeyID}
}

func sampleSeatChallengeResult() SeatChallengeResult {
	return SeatChallengeResult{ChallengeID: sampleChallID, Challenge: byteRun(0x60, 32), Nonce: byteRun(0x20, 32), ExpiresAt: sampleDay}
}

func sampleSeatLoginChallengeResult() SeatLoginChallengeResult {
	return SeatLoginChallengeResult{ChallengeID: sampleChallID, Challenge: byteRun(0x60, 32), Nonce: byteRun(0x40, 32),
		ExpiresAt: sampleDay, Allow: [][]byte{byteRun(0, 16), bytes.Repeat([]byte{0x41}, 32)}}
}

func sampleSeatDecideParams() SeatDecideParams {
	return SeatDecideParams{ChallengeID: sampleChallID, Frame: sampleFrame(), Assertion: sampleAssertion()}
}

func sampleSeatLoginParams() SeatLoginParams {
	return SeatLoginParams{ChallengeID: sampleChallID, Assertion: sampleAssertion()}
}

func sampleSeatLoginResult() SeatLoginResult {
	return SeatLoginResult{Credential: sampleKeyID, CredentialID: byteRun(0, 16), Kind: "vault-passkey"}
}

// The six operations are the gate's alone: the list of the Unix socket has
// none of them, and GateOperations lists them, in the order of the table.
func TestGateOperationsAreApartFromTheSocket(t *testing.T) {
	want := []string{"seat.login-challenge", "seat.login", "seat.challenge", "seat.decide", "seat.wait", "seat.genesis"}
	got := GateOperations()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GateOperations() = %v, want %v", got, want)
	}
	if consts := []string{OpSeatLoginChallenge, OpSeatLogin, OpSeatChallenge, OpSeatDecide, OpSeatWait, OpSeatGenesis}; !reflect.DeepEqual(consts, want) {
		t.Errorf("the constants are %v, want %v", consts, want)
	}
	socket := make(map[string]bool)
	for _, op := range Operations() {
		socket[op] = true
	}
	form := regexp.MustCompile(`^seat\.[a-z0-9-]+$`)
	for _, op := range got {
		if socket[op] {
			t.Errorf("%s is in Operations(), the list the Unix socket serves and announces", op)
		}
		if !form.MatchString(op) {
			t.Errorf("%q is not seat. and one part of lower-case letters, digits and dashes", op)
		}
	}
	got[0] = "changed"
	if again := GateOperations(); again[0] != OpSeatLoginChallenge {
		t.Fatalf("the list is shared: its first name is now %q", again[0])
	}
}

// The codes are words of the gate's grammar, each its own, and none is a
// refusal code of the record package.
func TestGateCodesAreWords(t *testing.T) {
	codes := map[string]string{
		CodeChallengeStale: "challenge-stale", CodeChallengesFull: "challenges-full",
		CodeAssertionInvalid: "assertion-invalid", CodeKeyRevoked: "key-revoked", CodeKindRefused: "kind-refused",
		CodeWaitFull: "wait-full",
	}
	if len(codes) != 6 {
		t.Fatalf("%d codes with different words, want 6", len(codes))
	}
	grammar := regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	for code, word := range codes {
		if code != word || !grammar.MatchString(code) {
			t.Errorf("the code %s is written %q", word, code)
		}
	}
}

// The vectors of the contract, computed by two programs that share no code.
func TestChallengeVectors(t *testing.T) {
	for name, tt := range map[string]struct{ got, want string }{
		"no sign-off":          {SignedOffDigest(nil), "df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119"},
		"an empty list":        {SignedOffDigest([]record.SignOff{}), "df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119"},
		"the two sign-offs":    {SignedOffDigest(vectorSignOffs()), "18b556eee20ab32197f868320c7c611c4001d680c69937b84a209bd7ec0d3899"},
		"an odd path":          {SignedOffDigest([]record.SignOff{{Kind: record.FlagOddPath, Path: "a<b>&c"}}), "827975e3705c3eb04dc1f5c08de133882b3de9442fc6e1ea1c928931db07c8ff"},
		"no reason":            {ReasonDigest(""), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		"a reason":             {ReasonDigest("not this one"), "f60aafe0413bd84763dd2d54f284df0bbf5e0cfefc84b0227f346cf492756e8a"},
		"the sample assertion": {sampleAssertion().Digest(), "7d6c723c90db49417f74b3e73ebc5c8abd774f0e0d942f82ebd4fb6784b60ac0"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s: %s, want %s", name, tt.got, tt.want)
		}
	}

	approve := DecisionFrame{ID: "ap-20261120-0007", BindingSHA256: vectorBinding, Decision: record.DecisionApprove, SignedOff: vectorSignOffs()}
	c, err := DecisionChallenge(vectorInstance, approve, byteRun(0x20, 32))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(c); got != "8f232e82c7b8dfac8f89b1587ebd03ea0b1bfa36f683932ade5f796600e5d20c" {
		t.Errorf("the approve's challenge is %s", got)
	}
	if got := base64.RawURLEncoding.EncodeToString(c); got != "jyMugse436yPibFYfr0D6gsb-jb2g5Mq3l95ZgDl0gw" {
		t.Errorf("the approve's challenge in client data is %s", got)
	}
	deny := DecisionFrame{ID: "ap-20261120-0007", BindingSHA256: vectorBinding, Decision: record.DecisionDeny, SignedOff: []record.SignOff{}, Reason: "not this one"}
	c, err = DecisionChallenge(vectorInstance, deny, byteRun(0x20, 32))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(c); got != "f55e409d4b13759dd6c7c4caae52bd1af767a006afd71e446e9995ce37d8184d" {
		t.Errorf("the deny's challenge is %s", got)
	}
	c, err = LoginChallenge(vectorInstance, byteRun(0x40, 32))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(c); got != "586b941d8d6d60ec7626a362dac9e97c777664ee647ecf1688121b7b1237fa8f" {
		t.Errorf("the login challenge is %s", got)
	}
}

// A challenge binds every field of the frame, the instance and the nonce: a
// change of any one gives another challenge. A login and a decision never
// share one.
func TestDecisionChallengeBindsEveryField(t *testing.T) {
	nonce := byteRun(0x20, 32)
	base := DecisionFrame{ID: "ap-20261120-0007", BindingSHA256: vectorBinding, Decision: record.DecisionApprove, SignedOff: vectorSignOffs()}
	want, err := DecisionChallenge(vectorInstance, base, nonce)
	if err != nil {
		t.Fatal(err)
	}
	other := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	changes := map[string]func(f *DecisionFrame, instance *string, nonce []byte){
		"another card":       func(f *DecisionFrame, _ *string, _ []byte) { f.ID = "ap-20261120-0008" },
		"another binding":    func(f *DecisionFrame, _ *string, _ []byte) { f.BindingSHA256 = other },
		"a deny":             func(f *DecisionFrame, _ *string, _ []byte) { f.Decision, f.SignedOff = record.DecisionDeny, nil },
		"one sign-off fewer": func(f *DecisionFrame, _ *string, _ []byte) { f.SignedOff = f.SignedOff[:1] },
		"the sign-offs reversed": func(f *DecisionFrame, _ *string, _ []byte) {
			f.SignedOff[0], f.SignedOff[1] = f.SignedOff[1], f.SignedOff[0]
		},
		"another path":     func(f *DecisionFrame, _ *string, _ []byte) { f.SignedOff[0].Path = "Makefile2" },
		"another kind":     func(f *DecisionFrame, _ *string, _ []byte) { f.SignedOff[0].Kind = record.FlagAliased },
		"another instance": func(_ *DecisionFrame, i *string, _ []byte) { *i = other },
		"another nonce":    func(_ *DecisionFrame, _ *string, n []byte) { n[31] ^= 1 },
	}
	for name, change := range changes {
		f := base
		f.SignedOff = append([]record.SignOff(nil), base.SignedOff...)
		instance, n := vectorInstance, append([]byte(nil), nonce...)
		change(&f, &instance, n)
		got, err := DecisionChallenge(instance, f, n)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if bytes.Equal(got, want) {
			t.Errorf("%s: the same challenge", name)
		}
	}
	// A reason and none differ, and so do two reasons.
	deny := DecisionFrame{ID: base.ID, BindingSHA256: vectorBinding, Decision: record.DecisionDeny}
	a, _ := DecisionChallenge(vectorInstance, deny, nonce)
	deny.Reason = "x"
	b, _ := DecisionChallenge(vectorInstance, deny, nonce)
	deny.Reason = "y"
	c, _ := DecisionChallenge(vectorInstance, deny, nonce)
	if bytes.Equal(a, b) || bytes.Equal(b, c) {
		t.Error("a reason does not change the challenge")
	}
	// A login challenge over the same instance and nonce is another.
	login, err := LoginChallenge(vectorInstance, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(login, want) || bytes.Equal(login, a) {
		t.Error("a login challenge equals a decision's")
	}

	// An instance or a nonce of another form, or a frame the daemon would
	// refuse, gives no challenge.
	for name, tt := range map[string]struct {
		instance string
		nonce    []byte
	}{
		"a short instance":         {vectorInstance[:63], nonce},
		"an instance in capitals":  {strings.ToUpper(vectorInstance), nonce},
		"a nonce of 31 bytes":      {vectorInstance, nonce[:31]},
		"a nonce of 33 bytes":      {vectorInstance, append(append([]byte(nil), nonce...), 0)},
		"no nonce":                 {vectorInstance, nil},
		"an instance that is text": {strings.Repeat("g", 64), nonce},
	} {
		_, err := DecisionChallenge(tt.instance, base, tt.nonce)
		refused(t, err, record.CodeRecordInvalid, "decision: "+name)
		_, err = LoginChallenge(tt.instance, tt.nonce)
		refused(t, err, record.CodeRecordInvalid, "login: "+name)
	}
	bad := base
	bad.Reason = "an approve with a reason"
	_, err = DecisionChallenge(vectorInstance, bad, nonce)
	refused(t, err, record.CodeDecisionInvalid, "an approve with a reason")
}

// The params of seat.decide are read in two parts. The envelope's reading
// takes a frame whose content is wrong, so that the daemon can burn the
// challenge first; the second part refuses it decision-invalid. What is
// wrong with the form of the frame is refused with the envelope.
func TestSeatDecideParamsAreReadInTwoParts(t *testing.T) {
	sample, err := EncodeSeatDecideParams(sampleSeatDecideParams())
	if err != nil {
		t.Fatal(err)
	}
	frame, err := EncodeDecisionFrame(sampleFrame())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(sample, frame) {
		t.Fatalf("the params do not hold the frame as owner.decide writes it: %s", sample)
	}
	with := func(f string) []byte { return bytes.Replace(sample, frame, []byte(f), 1) }
	base := `{"binding_sha256":"` + sampleDigest + `","id":"` + sampleID + `",`
	content := map[string]string{
		"an approve with a reason":    base + `"decision":"approve","signed_off":[],"reason":"because"}`,
		"a deny with sign-offs":       base + `"decision":"deny","signed_off":[{"kind":"exec-config","path":"Makefile"}]}`,
		"a reason of 501 bytes":       base + `"decision":"deny","signed_off":[],"reason":"` + strings.Repeat("r", 501) + `"}`,
		"a decision of another word":  base + `"decision":"maybe","signed_off":[]}`,
		"a sign-off of no known kind": base + `"decision":"approve","signed_off":[{"kind":"anything","path":"x"}]}`,
		"a sign-off twice":            base + `"decision":"approve","signed_off":[{"kind":"symlink","path":"x"},{"kind":"symlink","path":"x"}]}`,
		"an id outside the grammar":   `{"binding_sha256":"` + sampleDigest + `","id":"ap-../x","decision":"approve","signed_off":[]}`,
		"a short binding digest":      `{"binding_sha256":"` + sampleDigest[:63] + `","id":"` + sampleID + `","decision":"approve","signed_off":[]}`,
	}
	for name, f := range content {
		p, err := DecodeSeatDecideParams(with(f))
		if err != nil {
			t.Errorf("%s: the envelope is refused: %v", name, err)
			continue
		}
		if p.ChallengeID != sampleChallID || !reflect.DeepEqual(p.Assertion, sampleAssertion()) {
			t.Errorf("%s: the envelope read as %+v", name, p)
		}
		refused(t, CheckDecisionFrame(p.Frame), record.CodeDecisionInvalid, "the content of "+name)
		_, err = EncodeSeatDecideParams(p)
		refused(t, err, record.CodeDecisionInvalid, "writing "+name)
	}
	if err := CheckDecisionFrame(sampleFrame()); err != nil {
		t.Errorf("the sample frame's content is refused: %v", err)
	}

	form := map[string]string{
		"a frame with a daemon's field": base + `"decision":"approve","signed_off":[],"confirmed":"passkey"}`,
		"a frame with no decision":      base + `"signed_off":[]}`,
		"a frame with no sign-off list": base + `"decision":"approve"}`,
		"a decision that is true":       base + `"decision":true,"signed_off":[]}`,
		"a reason that is a number":     base + `"decision":"deny","signed_off":[],"reason":5}`,
		"an empty reason":               base + `"decision":"deny","signed_off":[],"reason":""}`,
		"a frame that is a list":        `[]`,
		"a reason with a control":       base + `"decision":"deny","signed_off":[],"reason":"a\u001bb"}`,
	}
	for name, f := range form {
		_, err := DecodeSeatDecideParams(with(f))
		refused(t, err, record.CodeRecordInvalid, name)
	}
	for name, in := range map[string][]byte{
		"no frame":                      bytes.Replace(sample, append([]byte(`,"frame":`), frame...), nil, 1),
		"no assertion":                  []byte(`{"challenge_id":"` + sampleChallID + `","frame":` + string(frame) + `}`),
		"no challenge id":               bytes.Replace(sample, []byte(`"challenge_id":"`+sampleChallID+`",`), nil, 1),
		"a challenge id in capitals":    bytes.Replace(sample, []byte(sampleChallID), []byte("00112233445566AA"), 1),
		"a challenge id of 15 digits":   bytes.Replace(sample, []byte(sampleChallID), []byte("001122334455667"), 1),
		"an assertion with a user id":   bytes.Replace(sample, []byte(`"assertion":{`), []byte(`"assertion":{"user_handle":"AA==",`), 1),
		"a signature that is not b64":   bytes.Replace(sample, []byte(`"signature":"`), []byte(`"signature":"*`), 1),
		"a credential id in base64url":  bytes.Replace(sample, []byte(`"credential_id":"AAECAwQFBgcICQoLDA0ODw=="`), []byte(`"credential_id":"AAECAwQFBgcICQoLDA0ODw"`), 1),
		"an assertion that is a string": bytes.Replace(sample, []byte(`"assertion":{`), []byte(`"assertion":"x","y":{`), 1),
	} {
		if bytes.Equal(in, sample) {
			t.Fatalf("%s: the sample did not change", name)
		}
		_, err := DecodeSeatDecideParams(in)
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

// Each field of an assertion is held to its bounds, in the params of
// seat.decide and of seat.login alike; the bounds themselves are taken.
func TestAnAssertionIsHeldToItsBounds(t *testing.T) {
	field := map[string]func(a *Assertion) *[]byte{
		"credential_id":      func(a *Assertion) *[]byte { return &a.CredentialID },
		"authenticator_data": func(a *Assertion) *[]byte { return &a.AuthenticatorData },
		"client_data_json":   func(a *Assertion) *[]byte { return &a.ClientDataJSON },
		"signature":          func(a *Assertion) *[]byte { return &a.Signature },
	}
	bounds := map[string][2]int{
		"credential_id":      {MinCredentialIDBytes, MaxCredentialIDBytes},
		"authenticator_data": {MinAuthenticatorDataBytes, MaxAuthenticatorDataBytes},
		"client_data_json":   {1, MaxClientDataBytes},
		"signature":          {1, MaxSignatureBytes},
	}
	if MinAuthenticatorDataBytes != 37 || MaxAuthenticatorDataBytes != 1024 || MaxClientDataBytes != 2048 || MaxSignatureBytes != 72 {
		t.Error("a bound of an assertion is not the one the contract fixes")
	}
	sampleLogin, err := EncodeSeatLoginParams(sampleSeatLoginParams())
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range bounds {
		for _, n := range []int{b[0], b[1], b[0] - 1, b[1] + 1} {
			what := name + " of " + strconv.Itoa(n) + " bytes"
			a := sampleAssertion()
			*field[name](&a) = bytes.Repeat([]byte{0x30}, n)
			decide := sampleSeatDecideParams()
			decide.Assertion = a
			login := sampleSeatLoginParams()
			login.Assertion = a
			// The encoders make the same check as the decoders.
			_, errD := EncodeSeatDecideParams(decide)
			_, errL := EncodeSeatLoginParams(login)
			if n >= b[0] && n <= b[1] {
				if errD != nil || errL != nil {
					t.Errorf("%s is refused: %v, %v", what, errD, errL)
				}
				continue
			}
			refused(t, errD, record.CodeRecordInvalid, "writing seat.decide: "+what)
			refused(t, errL, record.CodeRecordInvalid, "writing seat.login: "+what)
			// And the bytes a seat could send, which need not come from
			// the encoder.
			s := sampleAssertion()
			old := `"` + name + `":"` + base64.StdEncoding.EncodeToString(*field[name](&s)) + `"`
			in := bytes.Replace(sampleLogin, []byte(old), []byte(`"`+name+`":"`+base64.StdEncoding.EncodeToString(*field[name](&a))+`"`), 1)
			if bytes.Equal(in, sampleLogin) {
				t.Fatalf("%s: the sample did not change", what)
			}
			_, err = DecodeSeatLoginParams(in)
			refused(t, err, record.CodeRecordInvalid, "reading seat.login: "+what)
		}
	}
	if (Assertion{}).Digest() == sampleAssertion().Digest() {
		t.Error("two assertions share a digest")
	}
}

// The caps leave room for the largest payload of each form: the largest
// frame with the largest assertion, the largest login, and a login challenge
// that allows the most credentials of the longest id.
func TestTheSeatCapsHoldTheLargestPayloads(t *testing.T) {
	big := Assertion{
		CredentialID:      bytes.Repeat([]byte{1}, MaxCredentialIDBytes),
		AuthenticatorData: bytes.Repeat([]byte{2}, MaxAuthenticatorDataBytes),
		ClientDataJSON:    bytes.Repeat([]byte{3}, MaxClientDataBytes),
		Signature:         bytes.Repeat([]byte{4}, MaxSignatureBytes),
	}
	// The largest frame owner.decide takes.
	f := sampleFrame()
	f.SignedOff = nil
	var largest DecisionFrame
	for i := 0; ; i++ {
		f.SignedOff = append(f.SignedOff, record.SignOff{Kind: record.FlagOddPath, Path: record.Text("p" + strings.Repeat("\x01", 40) + string(rune('a'+i%26)) + strings.Repeat("z", i/26))})
		if _, err := EncodeDecisionFrame(f); err != nil {
			break
		}
		largest = f
		largest.SignedOff = append([]record.SignOff(nil), f.SignedOff...)
	}
	framed, err := EncodeDecisionFrame(largest)
	if err != nil || len(framed) < MaxFrameBytes-200 {
		t.Fatalf("the largest frame is %d bytes (%v), want just under %d", len(framed), err, MaxFrameBytes)
	}
	if out, err := EncodeSeatDecideParams(SeatDecideParams{ChallengeID: sampleChallID, Frame: largest, Assertion: big}); err != nil {
		t.Errorf("the largest decide params do not fit: %v", err)
	} else if _, err := DecodeSeatDecideParams(out); err != nil {
		t.Errorf("the largest decide params do not decode: %v", err)
	}
	if _, err := EncodeSeatChallengeParams(SeatChallengeParams{Frame: largest, Credential: sampleKeyID}); err != nil {
		t.Errorf("the largest challenge params do not fit: %v", err)
	}
	if _, err := EncodeSeatLoginParams(SeatLoginParams{ChallengeID: sampleChallID, Assertion: big}); err != nil {
		t.Errorf("the largest login params do not fit: %v", err)
	}
	r := sampleSeatLoginChallengeResult()
	r.Allow = nil
	for i := 0; i < MaxLoginAllow; i++ {
		r.Allow = append(r.Allow, bytes.Repeat([]byte{byte(i)}, MaxCredentialIDBytes))
	}
	if _, err := EncodeSeatLoginChallengeResult(r); err != nil {
		t.Errorf("the largest login challenge does not fit: %v", err)
	}
	if _, err := EncodeSeatLoginResult(SeatLoginResult{Credential: displayID(big.CredentialID), CredentialID: big.CredentialID, Kind: "passkey"}); err != nil {
		t.Errorf("the largest login result does not fit: %v", err)
	}
	if MaxSeatChallengeParamsBytes != MaxFrameBytes+1024 || MaxSeatDecideParamsBytes != MaxFrameBytes+8192 || MaxSeatLoginParamsBytes != 8192 {
		t.Error("a cap is not the one the contract fixes")
	}
}

func TestSeatChallengeParamsContent(t *testing.T) {
	for name, p := range map[string]SeatChallengeParams{
		"a short credential":         {Frame: sampleFrame(), Credential: sampleKeyID[:15]},
		"a credential in capitals":   {Frame: sampleFrame(), Credential: strings.ToUpper(sampleKeyID)},
		"no credential":              {Frame: sampleFrame()},
		"a credential prefix":        {Frame: sampleFrame(), Credential: sampleKeyID[:4]},
		"a credential that is a raw": {Frame: sampleFrame(), Credential: string(byteRun(0, 16))},
	} {
		_, err := EncodeSeatChallengeParams(p)
		refused(t, err, record.CodeRecordInvalid, name)
	}
	// Its params burn nothing, so the frame is read whole with them.
	bad := sampleSeatChallengeParams()
	bad.Frame.Decision = record.DecisionDeny
	_, err := EncodeSeatChallengeParams(bad)
	refused(t, err, record.CodeDecisionInvalid, "a deny with sign-offs")
	sample, _ := EncodeSeatChallengeParams(sampleSeatChallengeParams())
	_, err = DecodeSeatChallengeParams(bytes.Replace(sample, []byte(`"decision":"approve"`), []byte(`"decision":"deny"`), 1))
	refused(t, err, record.CodeDecisionInvalid, "decoding a deny with sign-offs")
}

func TestSeatResultsContent(t *testing.T) {
	results := map[string]func(*SeatChallengeResult){
		"a challenge of 31 bytes":  func(r *SeatChallengeResult) { r.Challenge = r.Challenge[:31] },
		"a challenge of 33 bytes":  func(r *SeatChallengeResult) { r.Challenge = append(r.Challenge, 0) },
		"a nonce of 31 bytes":      func(r *SeatChallengeResult) { r.Nonce = r.Nonce[:31] },
		"no nonce":                 func(r *SeatChallengeResult) { r.Nonce = nil },
		"a short challenge id":     func(r *SeatChallengeResult) { r.ChallengeID = r.ChallengeID[:15] },
		"a challenge id in upper":  func(r *SeatChallengeResult) { r.ChallengeID = "ABCDEF0123456789" },
		"an expiry with an offset": func(r *SeatChallengeResult) { r.ExpiresAt = "2026-11-20T06:06:40-08:00" },
		"no expiry":                func(r *SeatChallengeResult) { r.ExpiresAt = "" },
	}
	for name, change := range results {
		r := sampleSeatChallengeResult()
		change(&r)
		_, err := EncodeSeatChallengeResult(r)
		refused(t, err, record.CodeRecordInvalid, "challenge: "+name)
		l := sampleSeatLoginChallengeResult()
		l.ChallengeID, l.Challenge, l.Nonce, l.ExpiresAt = r.ChallengeID, r.Challenge, r.Nonce, r.ExpiresAt
		_, err = EncodeSeatLoginChallengeResult(l)
		refused(t, err, record.CodeRecordInvalid, "login challenge: "+name)
	}

	allows := map[string][][]byte{
		"one credential too many":  nil,
		"a credential twice":       {byteRun(0, 16), byteRun(0, 16)},
		"a short credential id":    {byteRun(0, 15)},
		"a credential id too long": {bytes.Repeat([]byte{1}, MaxCredentialIDBytes+1)},
	}
	for i := 0; i <= MaxLoginAllow; i++ {
		allows["one credential too many"] = append(allows["one credential too many"], bytes.Repeat([]byte{byte(i)}, 16))
	}
	for name, allow := range allows {
		l := sampleSeatLoginChallengeResult()
		l.Allow = allow
		_, err := EncodeSeatLoginChallengeResult(l)
		refused(t, err, record.CodeRecordInvalid, "allow: "+name)
	}
	l := sampleSeatLoginChallengeResult()
	l.Allow = [][]byte{}
	if out, err := EncodeSeatLoginChallengeResult(l); err != nil {
		t.Errorf("a login challenge that allows none is refused: %v", err)
	} else if _, err := DecodeSeatLoginChallengeResult(out); err != nil {
		t.Errorf("a login challenge that allows none does not decode: %v", err)
	}

	logins := map[string]func(*SeatLoginResult){
		"another credential's display id": func(r *SeatLoginResult) { r.Credential = "0123456789abcdef" },
		"a display id in capitals":        func(r *SeatLoginResult) { r.Credential = strings.ToUpper(r.Credential) },
		"a short credential id": func(r *SeatLoginResult) {
			r.CredentialID = r.CredentialID[:15]
			r.Credential = displayID(r.CredentialID)
		},
		"a credential id too long": func(r *SeatLoginResult) {
			r.CredentialID = bytes.Repeat([]byte{1}, MaxCredentialIDBytes+1)
			r.Credential = displayID(r.CredentialID)
		},
		"no kind":             func(r *SeatLoginResult) { r.Kind = "" },
		"a kind in capitals":  func(r *SeatLoginResult) { r.Kind = "Passkey" },
		"a kind with a space": func(r *SeatLoginResult) { r.Kind = "vault passkey" },
	}
	for name, change := range logins {
		r := sampleSeatLoginResult()
		change(&r)
		_, err := EncodeSeatLoginResult(r)
		refused(t, err, record.CodeRecordInvalid, "login: "+name)
	}
	sample, _ := EncodeSeatLoginResult(sampleSeatLoginResult())
	for name, id := range map[string]string{"no challenge id": "", "a short challenge id": sampleChallID[:15], "a challenge id in capitals": "ABCDEF0123456789"} {
		p := sampleSeatLoginParams()
		p.ChallengeID = id
		_, err := EncodeSeatLoginParams(p)
		refused(t, err, record.CodeRecordInvalid, "login params: "+name)
	}
	_, err := DecodeSeatLoginResult(bytes.Replace(sample, []byte(sampleKeyID), []byte("0123456789abcdef"), 1))
	refused(t, err, record.CodeRecordInvalid, "decoding another credential's display id")
}

// The samples of seat.wait. The ids and bindings are those of the vectors in
// TestPendingVersionVectors.
const (
	waitIDA      = "ap-20261120-0007"
	waitIDB      = "ap-20261120-0008"
	waitBindingB = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	// waitVersionTwo is the version of sampleWaitCards.
	waitVersionTwo = "9ac6d04d66be695b1abd73a004de6e86"
)

func sampleWaitCards() []SeatWaitCard {
	return []SeatWaitCard{
		{ID: waitIDA, Kind: CardKindApproval, BindingSHA256: vectorBinding},
		{ID: waitIDB, Kind: CardKindApproval, BindingSHA256: waitBindingB, Held: true},
	}
}

func sampleSeatWaitParams() SeatWaitParams { return SeatWaitParams{Since: waitVersionTwo} }

func sampleSeatWaitResult() SeatWaitResult {
	return SeatWaitResult{Version: waitVersionTwo, Waiting: 2, Cards: sampleWaitCards()}
}

// The version of a pending set is fixed by vectors computed by a program
// that shares no code with it: SHA-256 over the domain string, the count and
// each card, every field with a 4-byte big-endian length, the held mark one
// byte, the first 16 bytes in hexadecimal.
func TestPendingVersionVectors(t *testing.T) {
	card := func(kind, id, binding string, held bool) SeatWaitCard {
		return SeatWaitCard{ID: id, Kind: kind, BindingSHA256: binding, Held: held}
	}
	for name, tt := range map[string]struct {
		set  []SeatWaitCard
		want string
	}{
		"the empty set":             {nil, "ad9ab6441ef51d741566db3a72c3d088"},
		"an empty list":             {[]SeatWaitCard{}, "ad9ab6441ef51d741566db3a72c3d088"},
		"one card":                  {[]SeatWaitCard{card("approval", waitIDA, vectorBinding, false)}, "4d40d4c0e4ff366ac1b62019f799a449"},
		"one card, held":            {[]SeatWaitCard{card("approval", waitIDA, vectorBinding, true)}, "99b76dfd3da8c560e9be01c48bbdc8d2"},
		"one card, another kind":    {[]SeatWaitCard{card("question", waitIDA, vectorBinding, false)}, "486c4469e73a8ee44207a485702e9e2a"},
		"one card, another binding": {[]SeatWaitCard{card("approval", waitIDA, waitBindingB, false)}, "9fb531da0d7e401198df16648ec04c63"},
		"two cards":                 {sampleWaitCards(), waitVersionTwo},
	} {
		got := PendingVersion(tt.set)
		if got != tt.want {
			t.Errorf("%s: %s, want %s", name, got, tt.want)
		}
		if !record.IsLowerHex(got, SeatWaitVersionLength) {
			t.Errorf("%s: %q is not %d lower-case hexadecimal digits", name, got, SeatWaitVersionLength)
		}
	}
}

// The order the cards come in does not change the version, and the call does
// not reorder what it was given.
func TestPendingVersionIgnoresOrderAndKeepsItsInput(t *testing.T) {
	var set []SeatWaitCard
	for i := 0; i < 9; i++ {
		set = append(set, SeatWaitCard{ID: "ap-" + strconv.Itoa(100-i), Kind: CardKindApproval,
			BindingSHA256: record.Digest([]byte{byte(i)}), Held: i%3 == 0})
	}
	// Two cards of one id and binding, apart only by the held mark and the kind.
	set = append(set, SeatWaitCard{ID: "ap-1", Kind: "approval", BindingSHA256: vectorBinding, Held: true},
		SeatWaitCard{ID: "ap-1", Kind: "approval", BindingSHA256: vectorBinding},
		SeatWaitCard{ID: "ap-1", Kind: "question", BindingSHA256: vectorBinding})
	want := PendingVersion(set)
	kept := append([]SeatWaitCard(nil), set...)
	for shift := 1; shift < len(set); shift++ {
		turned := append(append([]SeatWaitCard(nil), set[shift:]...), set[:shift]...)
		if got := PendingVersion(turned); got != want {
			t.Fatalf("turned by %d: %s, want %s", shift, got, want)
		}
	}
	reversed := make([]SeatWaitCard, len(set))
	for i := range set {
		reversed[len(set)-1-i] = set[i]
	}
	if got := PendingVersion(reversed); got != want {
		t.Errorf("reversed: %s, want %s", got, want)
	}
	if !reflect.DeepEqual(set, kept) {
		t.Error("PendingVersion reordered the slice it was given")
	}
}

// Each field of a card, the count and the cards themselves are in the
// version: a set that differs in any of them has another one.
func TestPendingVersionBindsEveryField(t *testing.T) {
	base := sampleWaitCards()
	want := PendingVersion(base)
	changes := map[string]func([]SeatWaitCard) []SeatWaitCard{
		"a binding":           func(s []SeatWaitCard) []SeatWaitCard { s[0].BindingSHA256 = waitBindingB; return s },
		"a binding digit":     func(s []SeatWaitCard) []SeatWaitCard { s[1].BindingSHA256 = s[1].BindingSHA256[:63] + "f"; return s },
		"a held mark set":     func(s []SeatWaitCard) []SeatWaitCard { s[0].Held = true; return s },
		"a held mark cleared": func(s []SeatWaitCard) []SeatWaitCard { s[1].Held = false; return s },
		"a kind":              func(s []SeatWaitCard) []SeatWaitCard { s[0].Kind = "question"; return s },
		"an id":               func(s []SeatWaitCard) []SeatWaitCard { s[0].ID = "ap-20261120-0009"; return s },
		"a card less":         func(s []SeatWaitCard) []SeatWaitCard { return s[:1] },
		"a card more": func(s []SeatWaitCard) []SeatWaitCard {
			return append(s, SeatWaitCard{ID: "ap-x", Kind: CardKindApproval, BindingSHA256: vectorBinding})
		},
		"a card twice": func(s []SeatWaitCard) []SeatWaitCard { return append(s, s[0]) },
		"no card":      func(s []SeatWaitCard) []SeatWaitCard { return nil },
	}
	seen := map[string]string{want: "the base set"}
	for name, change := range changes {
		got := PendingVersion(change(append([]SeatWaitCard(nil), base...)))
		if got == want {
			t.Errorf("%s does not change the version", name)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%s and %s have the same version", name, other)
		}
		seen[got] = name
	}
}

// The fields are length-prefixed, so a boundary moved between two fields, or
// between two cards, gives another version. Without the prefixes these sets
// would write the same bytes.
func TestPendingVersionIsNotACatenation(t *testing.T) {
	sets := map[string][]SeatWaitCard{
		"the kind takes the id's first byte": {{Kind: "approval", ID: "ap-1", BindingSHA256: "ab"}},
		"the kind is a byte longer":          {{Kind: "approvala", ID: "p-1", BindingSHA256: "ab"}},
		"the id takes the binding's first":   {{Kind: "approval", ID: "ap-1a", BindingSHA256: "b"}},
		"the id takes all of the binding":    {{Kind: "approval", ID: "ap-1ab", BindingSHA256: ""}},
		"two cards":                          {{Kind: "k", ID: "a", BindingSHA256: "b"}, {Kind: "c", ID: "d", BindingSHA256: "e"}},
		"two cards, a boundary moved":        {{Kind: "k", ID: "a", BindingSHA256: "bc"}, {Kind: "", ID: "d", BindingSHA256: "e"}},
		"one card of the same letters":       {{Kind: "k", ID: "ab", BindingSHA256: "cd"}},
		"empty fields":                       {{}},
		"two empty cards":                    {{}, {}},
		"no card":                            nil,
	}
	seen := map[string]string{}
	for name, set := range sets {
		v := PendingVersion(set)
		if other, dup := seen[v]; dup {
			t.Errorf("%s and %s have the same version %s", name, other, v)
		}
		seen[v] = name
	}
}

func TestSeatWaitParamsContent(t *testing.T) {
	for name, since := range map[string]string{
		"a version in capitals": strings.ToUpper(waitVersionTwo),
		"a short version":       waitVersionTwo[:31],
		"a long version":        waitVersionTwo + "0",
		"a digest":              vectorBinding,
		"not hexadecimal":       strings.Repeat("g", 32),
		"a space":               " " + waitVersionTwo[1:],
	} {
		_, err := EncodeSeatWaitParams(SeatWaitParams{Since: since})
		refused(t, err, record.CodeRecordInvalid, "since: "+name)
		_, err = DecodeSeatWaitParams([]byte(`{"since":"` + since + `"}`))
		refused(t, err, record.CodeRecordInvalid, "decoding since: "+name)
	}
	for name, since := range map[string]string{"none": "", "a version": waitVersionTwo} {
		out, err := EncodeSeatWaitParams(SeatWaitParams{Since: since})
		if err != nil {
			t.Errorf("since: %s is refused: %v", name, err)
			continue
		}
		if want := `{"since":"` + since + `"}`; string(out) != want {
			t.Errorf("since: %s is written %s, want %s", name, out, want)
		}
		if p, err := DecodeSeatWaitParams(out); err != nil || p.Since != since {
			t.Errorf("since: %s reads back as %+v (%v)", name, p, err)
		}
	}
	// The field is always written: params without it are refused.
	for _, in := range []string{`{}`, `{"since":null}`, `{"since":1}`, `{"since":"` + waitVersionTwo + `","also":1}`} {
		_, err := DecodeSeatWaitParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, in)
	}
}

func TestSeatWaitResultContent(t *testing.T) {
	many := func(n int) []SeatWaitCard {
		cards := make([]SeatWaitCard, n)
		for i := range cards {
			cards[i] = SeatWaitCard{ID: "ap-" + strconv.Itoa(i+1), Kind: CardKindApproval, BindingSHA256: vectorBinding}
		}
		return cards
	}
	results := map[string]func(*SeatWaitResult){
		"no version":                  func(r *SeatWaitResult) { r.Version = "" },
		"a version in capitals":       func(r *SeatWaitResult) { r.Version = strings.ToUpper(r.Version) },
		"a short version":             func(r *SeatWaitResult) { r.Version = r.Version[:31] },
		"a digest as the version":     func(r *SeatWaitResult) { r.Version = vectorBinding },
		"a negative count":            func(r *SeatWaitResult) { r.Waiting = -1 },
		"fewer waiting than cards":    func(r *SeatWaitResult) { r.Waiting = 1 },
		"more said, none more":        func(r *SeatWaitResult) { r.More = true },
		"more waiting, more not said": func(r *SeatWaitResult) { r.Waiting = 3 },
		"more than the cap of cards": func(r *SeatWaitResult) {
			r.Cards, r.Waiting, r.More = many(MaxSeatWaitCards+1), MaxSeatWaitCards+1, false
		},
		"more than the cap, counted": func(r *SeatWaitResult) {
			r.Cards, r.Waiting, r.More = many(MaxSeatWaitCards+1), MaxSeatWaitCards+5, true
		},
		"a card twice":                   func(r *SeatWaitResult) { r.Cards[1].ID = r.Cards[0].ID },
		"no id":                          func(r *SeatWaitResult) { r.Cards[0].ID = "" },
		"an id that is not a pending id": func(r *SeatWaitResult) { r.Cards[0].ID = "rq-1" },
		"an id in capitals":              func(r *SeatWaitResult) { r.Cards[0].ID = "ap-ABC" },
		"no kind":                        func(r *SeatWaitResult) { r.Cards[0].Kind = "" },
		"a kind in capitals":             func(r *SeatWaitResult) { r.Cards[0].Kind = "Approval" },
		"a kind with a space":            func(r *SeatWaitResult) { r.Cards[0].Kind = "an approval" },
		"a kind too long":                func(r *SeatWaitResult) { r.Cards[0].Kind = strings.Repeat("k", MaxSeatWaitKindBytes+1) },
		"no binding":                     func(r *SeatWaitResult) { r.Cards[0].BindingSHA256 = "" },
		"a short binding":                func(r *SeatWaitResult) { r.Cards[0].BindingSHA256 = vectorBinding[:63] },
		"a binding in capitals":          func(r *SeatWaitResult) { r.Cards[0].BindingSHA256 = strings.ToUpper(vectorBinding) },
	}
	for name, change := range results {
		r := sampleSeatWaitResult()
		change(&r)
		_, err := EncodeSeatWaitResult(r)
		refused(t, err, record.CodeRecordInvalid, "wait result: "+name)
	}
	// A kind of the longest length, no cards, and more cards than listed are fine.
	for name, r := range map[string]SeatWaitResult{
		"no card":            {Version: PendingVersion(nil), Cards: []SeatWaitCard{}},
		"a nil list":         {Version: PendingVersion(nil)},
		"the longest kind":   {Version: waitVersionTwo, Waiting: 1, Cards: []SeatWaitCard{{ID: waitIDA, Kind: strings.Repeat("k", MaxSeatWaitKindBytes), BindingSHA256: vectorBinding}}},
		"the most cards":     {Version: waitVersionTwo, Waiting: MaxSeatWaitCards, Cards: many(MaxSeatWaitCards)},
		"more than listed":   {Version: waitVersionTwo, Waiting: 4000, Cards: many(MaxSeatWaitCards), More: true},
		"waiting, no listed": {Version: waitVersionTwo, Waiting: 3, More: true},
	} {
		out, err := EncodeSeatWaitResult(r)
		if err != nil {
			t.Errorf("wait result: %s is refused: %v", name, err)
			continue
		}
		if _, err := DecodeSeatWaitResult(out); err != nil {
			t.Errorf("wait result: %s does not decode: %v", name, err)
		}
	}
	// The members are all required.
	sample, _ := EncodeSeatWaitResult(sampleSeatWaitResult())
	for _, key := range []string{`"version"`, `"waiting"`, `"cards"`, `"more"`, `"binding_sha256"`, `"held"`, `"kind"`} {
		in := bytes.Replace(sample, []byte(key), []byte(`"x`+key[1:]), 1)
		_, err := DecodeSeatWaitResult(in)
		refused(t, err, record.CodeRecordInvalid, "wait result without "+key)
	}
}

// The result's own cap, about 24 KiB, holds the longest result: the most
// cards, each of the longest id and kind and a mark that writes longest.
func TestSeatWaitResultCapHoldsTheLargestResult(t *testing.T) {
	longest := SeatWaitResult{Version: waitVersionTwo, Waiting: 1 << 20, More: true}
	for i := 0; i < MaxSeatWaitCards; i++ {
		// 51 bytes, the longest pending id, and each one its own.
		id := "ap-" + strings.Repeat("z", 48-len(strconv.Itoa(i))) + strconv.Itoa(i)
		longest.Cards = append(longest.Cards, SeatWaitCard{ID: id, Kind: strings.Repeat("k", MaxSeatWaitKindBytes), BindingSHA256: vectorBinding})
	}
	out, err := EncodeSeatWaitResult(longest)
	if err != nil {
		t.Fatalf("the longest result does not fit: %v", err)
	}
	if _, err := DecodeSeatWaitResult(out); err != nil {
		t.Errorf("the longest result does not decode: %v", err)
	}
	t.Logf("longest result: %d bytes, id %d", len(out), len(longest.Cards[0].ID))
	if MaxSeatWaitResultBytes != 24<<10 || MaxSeatWaitCards != 128 {
		t.Error("a cap is not the one the plan fixes")
	}
	if len(out) < MaxSeatWaitResultBytes-2<<10 {
		t.Errorf("the longest result is %d bytes, far under the cap %d: the cap is looser than it says", len(out), MaxSeatWaitResultBytes)
	}
	// A result over the cap is refused before it is read.
	over := append(append([]byte(nil), out...), bytes.Repeat([]byte(" "), MaxSeatWaitResultBytes)...)
	_, err = DecodeSeatWaitResult(over)
	refused(t, err, record.CodeRecordTooLarge, "a wait result over its cap")
}
