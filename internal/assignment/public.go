package assignment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"
)

// Schema-1 members are declared in RFC 8785 key order. Public bindings
// identify holders without exporting private conversation or launch data.
type PublicBinding struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}
type Instance struct {
	Authority   string `json:"authority"`
	Incarnation string `json:"incarnation"`
	Store       string `json:"store"`
}
type PublicEnvelope struct {
	ActiveTransferID     string        `json:"active_transfer_id"`
	Admission            bool          `json:"admission"`
	Algorithm            string        `json:"alg"`
	Binding              PublicBinding `json:"binding"`
	CommitRevision       string        `json:"commit_revision"`
	EventID              string        `json:"event_id"`
	EvidenceDigests      []string      `json:"evidence_digests"`
	ExecutingInstance    Instance      `json:"executing_instance"`
	Format               string        `json:"format"`
	Generation           string        `json:"generation"`
	Home                 Instance      `json:"home"`
	InventoryDigest      string        `json:"inventory_digest"`
	KeyID                string        `json:"key_id"`
	Kind                 Kind          `json:"kind"`
	OldBinding           PublicBinding `json:"old_binding"`
	Phase                Phase         `json:"phase"`
	PreviousGeneration   string        `json:"previous_generation"`
	PreviousPublicDigest string        `json:"previous_public_digest"`
	PreviousRevision     string        `json:"previous_revision"`
	ProjectID            string        `json:"project_id"`
	RequestID            string        `json:"request_id"`
	Revision             string        `json:"revision"`
	RoleID               string        `json:"role_id"`
	Schema               uint64        `json:"schema"`
	Signature            string        `json:"signature,omitempty"`
	TransferID           string        `json:"transfer_id"`
}

func project(h Header, prior, row Row, tr Transfer, e Event) PublicEnvelope {
	digests := make([]string, 0, len(e.Command.Evidence))
	for _, proof := range e.Command.Evidence {
		digests = append(digests, proof.Digest)
	}
	return PublicEnvelope{
		ActiveTransferID: row.Head.TransferID, Format: Format,
		Admission: row.Permits(row.Head), Algorithm: "Ed25519", Binding: PublicBinding{row.Holder.ID, row.Holder.Kind},
		CommitRevision: strconv.FormatUint(row.CommitRevision, 10), EventID: e.Result.EventID, EvidenceDigests: digests,
		ExecutingInstance: Instance{row.Holder.Authority, row.Holder.Incarnation, row.Holder.Store},
		Generation:        strconv.FormatUint(row.Head.Generation, 10), Home: Instance{h.Authority, h.Incarnation, h.Store},
		InventoryDigest: tr.Inventory.Digest, KeyID: h.KeyID, Kind: e.Command.Kind, OldBinding: PublicBinding{prior.Holder.ID, prior.Holder.Kind},
		Phase: row.Head.Phase, PreviousGeneration: strconv.FormatUint(prior.Head.Generation, 10), PreviousPublicDigest: prior.PublicDigest,
		PreviousRevision: strconv.FormatUint(prior.Head.Revision, 10), ProjectID: row.Head.Key.Project, RequestID: e.Command.RequestID,
		Revision: strconv.FormatUint(row.Head.Revision, 10), RoleID: row.Head.Key.Role, Schema: e.Schema, TransferID: e.Command.TransferID,
	}
}

// canonicalJSON accepts fixed schemas with members in key order. RFC 8785
// emits U+2028/2029 literally. Preserve escaped backslashes before a "u".
func canonicalJSON(v any) ([]byte, error) {
	remaining := recordLimit
	if err := checkStrings(reflect.ValueOf(v), &remaining); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
	// The stock encoder only over-escapes U+2028/2029 here.
	if !bytes.Contains(b, []byte("\\u202")) {
		if len(b) > recordLimit {
			return nil, ErrLimit
		}
		return b, nil
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if b[i] != '\\' {
			out = append(out, b[i])
			i++
			continue
		}
		switch {
		case bytes.HasPrefix(b[i:], []byte("\\u2028")):
			out = append(out, "\u2028"...)
			i += 6
		case bytes.HasPrefix(b[i:], []byte("\\u2029")):
			out = append(out, "\u2029"...)
			i += 6
		default:
			out = append(out, b[i:i+2]...)
			i += 2
		}
	}
	if len(out) > recordLimit {
		return nil, ErrLimit
	}
	return out, nil
}
func (e Event) SigningBytes() ([]byte, error) {
	p := e.Public
	p.Signature = ""
	b, err := canonicalJSON(p)
	if err != nil {
		return nil, err
	}
	return append([]byte("ataniq.assignment.event.v1\x00"), b...), nil
}
func (e Event) SignedPublicBytes() ([]byte, error) {
	if e.Signature == "" {
		return nil, ErrChain
	}
	p := e.Public
	p.Signature = e.Signature
	return canonicalJSON(p)
}
func sha256Hex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
