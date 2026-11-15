package appcontract

import "ataniq/internal/appmodel"

// OwnerPrincipalRef names the stable principal independently of enrollments.
type OwnerPrincipalRef struct {
	Instance         Instance
	OwnerPrincipalID string
}
type BootstrapProvenance struct {
	BootstrapID                          [16]byte
	Owner                                OwnerPrincipalRef
	InitialClientID, InitialCredentialID string
	InitialCredentialGeneration          uint64
}
type AdmissionAudit struct {
	ActingClientID, AuthCredentialID string
	AuthGeneration                   uint64
	AdminPrincipal                   *OwnerPrincipalRef
}

// AdminResult records data; it does not establish authentication or disclosure.
type AdminResult struct {
	Command                    appmodel.CommandKey
	Digest                     [32]byte
	Resource                   Resource
	Revision                   appmodel.Revision
	Cursor                     string
	Outcome                    Outcome
	Artifact                   *ArtifactRef
	GrantedAdministrationScope *AdministrationScope
}
