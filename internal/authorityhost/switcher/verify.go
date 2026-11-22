package switcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SignatureNamespace is the ssh-keygen signature namespace of a release's
// SHA256SUMS. A release is signed with
//
//	ssh-keygen -Y sign -f KEY -n authority-host-release SHA256SUMS
const SignatureNamespace = "authority-host-release"

// signatureTimeout bounds each ssh-keygen run.
const signatureTimeout = 30 * time.Second

// Verifier checks the signature of a release's SHA256SUMS.
type Verifier interface {
	// Verify reports whether signaturePath is a good signature of data by a
	// signer named in allowedSigners.
	Verify(ctx context.Context, allowedSigners string, data []byte, signaturePath string) error
}

// SSHVerifier verifies with ssh-keygen -Y, through a Runner (so with a clean
// environment and a fixed argument vector). It first asks ssh-keygen which
// principal of the allowed signers file made the signature, then verifies the
// data against that principal in SignatureNamespace.
type SSHVerifier struct {
	Runner  Runner
	Program string // default "ssh-keygen", looked up in SearchPath
}

func (v SSHVerifier) program() string {
	if v.Program != "" {
		return v.Program
	}
	return "ssh-keygen"
}

func (v SSHVerifier) Verify(ctx context.Context, allowedSigners string, data []byte, signaturePath string) error {
	out, err := v.Runner.Run(ctx, Command{
		Argv:    []string{v.program(), "-Y", "find-principals", "-s", signaturePath, "-f", allowedSigners},
		Timeout: signatureTimeout,
	})
	if err != nil {
		return fmt.Errorf("no allowed signer made this signature: ssh-keygen -Y find-principals: %s", summary(err))
	}
	principal := ""
	for _, line := range strings.Split(string(out.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			principal = line
			break
		}
	}
	if principal == "" || strings.ContainsAny(principal, " \t") {
		return errors.New("ssh-keygen named no usable principal for the signature")
	}
	_, err = v.Runner.Run(ctx, Command{
		Argv:    []string{v.program(), "-Y", "verify", "-f", allowedSigners, "-I", principal, "-n", SignatureNamespace, "-s", signaturePath},
		Stdin:   data,
		Timeout: signatureTimeout,
	})
	if err != nil {
		return fmt.Errorf("signature does not verify: ssh-keygen -Y verify: %s", summary(err))
	}
	return nil
}
