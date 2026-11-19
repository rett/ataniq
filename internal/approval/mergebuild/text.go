package mergebuild

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strconv"
)

// commitText returns the whole text of the merge commit for a tree and the
// inputs, which have passed [Inputs.check]:
//
//	tree <tree>
//	parent <the target's tip>
//	parent <the feature's tip>
//	author <name> <<address>> <seconds> +0000
//	committer <name> <<address>> <seconds> +0000
//
//	Merge feature/<feature> into <target>
//
//	Request-Id: <request id>
//
// Every line ends with a newline, the last one too. There is no other
// header and no other line, and no byte comes from anywhere but the
// arguments.
func commitText(tree string, in Inputs) []byte {
	who := in.Identity.Name + " <" + in.Identity.Email + "> " + strconv.FormatInt(in.At.Unix(), 10) + " +0000\n"
	return []byte("tree " + tree + "\n" +
		"parent " + in.TargetTip + "\n" +
		"parent " + in.FeatureTip + "\n" +
		"author " + who +
		"committer " + who +
		"\n" +
		"Merge feature/" + in.Feature + " into " + in.Target + "\n" +
		"\n" +
		"Request-Id: " + in.RequestID + "\n")
}

// commitID returns the id git gives a commit with this text in a repository
// whose ids have idLen hexadecimal digits: the digest of the object's header
// and the text.
func commitID(idLen int, text []byte) string {
	var h hash.Hash
	if idLen == 64 {
		h = sha256.New()
	} else {
		h = sha1.New()
	}
	h.Write([]byte("commit " + strconv.Itoa(len(text)) + "\x00"))
	h.Write(text)
	return hex.EncodeToString(h.Sum(nil))
}
