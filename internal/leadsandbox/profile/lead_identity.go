package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

const authorityTagLen = 16

var authorityTagChars = regexp.MustCompile(`^[a-z0-9]+$`)

// ShortAuthorityTag takes the first sixteen hex digits of a public authority fingerprint.
func ShortAuthorityTag(authority string) (string, error) {
	if err := checkSHA256("instance authority", authority); err != nil {
		return "", err
	}
	return authority[:authorityTagLen], nil
}

// QualifiedLeadIdentity names a managed lead from the public tag in its launch inputs.
func QualifiedLeadIdentity(project, tag string) (string, error) {
	if err := CheckName("project", project); err != nil {
		return "", err
	}
	if len(tag) != authorityTagLen || !authorityTagChars.MatchString(tag) {
		return "", refuse("authority tag", "must be %d characters of [a-z0-9]", authorityTagLen)
	}
	const separator = "-lead-"
	const projectLimit = maxLeadIdentityLen - len(separator) - authorityTagLen
	if len(project) > projectLimit {
		// Keep long projects distinguishable even when their prefixes match.
		const projectHashBytes = 8
		sum := sha256.Sum256([]byte(project))
		hash := hex.EncodeToString(sum[:projectHashBytes])
		project = project[:projectLimit-1-len(hash)] + "-" + hash
	}
	return project + separator + tag, nil
}

// Qargi reads the label from the right as <lead>:coord:<feature>:<n>.
// Ataniq coordinator identities also carry the parent epoch on the lead.
var coordinatorLabel = regexp.MustCompile(`^([a-z0-9-]+)(@e[1-9][0-9]*)?:coord:([a-z0-9]+(-[a-z0-9]+)*):([1-9][0-9]*)$`)

// coordinatorActor binds a declared coordinator label to the parent lead
// used in this sandbox. Preserve the registry epoch, feature and instance.
func (in Inputs) coordinatorActor(label string) (string, error) {
	if in.LeadIdentity == "" {
		return "", refuse(EnvQargiActor, "requires the parent lead identity")
	}
	parts := coordinatorLabel.FindStringSubmatch(label)
	if parts == nil || len(parts[3]) > 40 {
		return "", refuse(EnvQargiActor, "expected a coordinator label with a kebab-case feature of at most 40 bytes and a positive instance number")
	}
	if parts[1] != in.Project+"-lead" && parts[1] != in.LeadIdentity {
		return "", refuse(EnvQargiActor, "the coordinator label belongs to another lead")
	}
	actor := in.LeadIdentity + label[len(parts[1]):]
	if len(actor) > 128 {
		return "", refuse(EnvQargiActor, "the qualified coordinator label exceeds 128 bytes")
	}
	return actor, nil
}
