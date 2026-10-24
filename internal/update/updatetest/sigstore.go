// Package updatetest signs fake releases for tests and the local release
// server spike. It runs a private in-memory Sigstore instance (Fulcio CA,
// Rekor log and timestamp authority from sigstore-go's test kit), so the
// bundles it writes go through the real verifier: only the trusted root
// differs from production. Never imported by the app.
package updatetest

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	v1 "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"google.golang.org/protobuf/encoding/protojson"
)

// Signer is a private Sigstore instance.
type Signer struct{ vs *ca.VirtualSigstore }

func NewSigner() (*Signer, error) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		return nil, err
	}
	return &Signer{vs: vs}, nil
}

// TrustedRoot is the instance's trust material.
func (s *Signer) TrustedRoot() root.TrustedMaterial { return s.vs }

// TrustedRootJSON serialises the trust material like trusted_root.json.
func (s *Signer) TrustedRootJSON() ([]byte, error) {
	tr, err := root.NewTrustedRoot(root.TrustedRootMediaType01,
		s.vs.FulcioCertificateAuthorities(), rawKeys(s.vs.CTLogs()), s.vs.TimestampingAuthorities(), rawKeys(s.vs.RekorLogs()))
	if err != nil {
		return nil, err
	}
	return tr.MarshalJSON()
}

// rawKeys gives each log its raw ID: the kit stores the hex text as the
// ID, which the trusted root would serialise as the ID's bytes.
func rawKeys(logs map[string]*root.TransparencyLog) map[string]*root.TransparencyLog {
	out := make(map[string]*root.TransparencyLog, len(logs))
	for k, v := range logs {
		c := *v
		if raw, err := hex.DecodeString(k); err == nil {
			c.ID = raw
		}
		out[k] = &c
	}
	return out
}

// SignBundle signs artifact with a keyless certificate for identity (the
// SAN URI) from issuer and returns a Sigstore bundle v0.3 in JSON, the
// format of checksums.txt.sigstore.json.
func (s *Signer) SignBundle(identity, issuer string, artifact []byte) ([]byte, error) {
	e, err := s.vs.Sign(identity, issuer, artifact)
	if err != nil {
		return nil, err
	}
	vc, err := e.VerificationContent()
	if err != nil {
		return nil, err
	}
	cert := vc.Certificate()
	if cert == nil {
		return nil, fmt.Errorf("updatetest: no certificate")
	}
	sc, err := e.SignatureContent()
	if err != nil {
		return nil, err
	}
	ms, ok := sc.(*bundle.MessageSignature)
	if !ok {
		return nil, fmt.Errorf("updatetest: not a message signature")
	}
	entries, err := e.TlogEntries()
	if err != nil {
		return nil, err
	}
	var tles []*v1.TransparencyLogEntry
	for _, en := range entries {
		tle, err := s.logEntry(en)
		if err != nil {
			return nil, err
		}
		tles = append(tles, tle)
	}
	tss, err := e.Timestamps()
	if err != nil {
		return nil, err
	}
	var rfc []*protocommon.RFC3161SignedTimestamp
	for _, ts := range tss {
		rfc = append(rfc, &protocommon.RFC3161SignedTimestamp{SignedTimestamp: ts})
	}
	pb := &protobundle.Bundle{
		MediaType: "application/vnd.dev.sigstore.bundle.v0.3+json",
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content:                   &protobundle.VerificationMaterial_Certificate{Certificate: &protocommon.X509Certificate{RawBytes: cert.Raw}},
			TlogEntries:               tles,
			TimestampVerificationData: &protobundle.TimestampVerificationData{Rfc3161Timestamps: rfc},
		},
		Content: &protobundle.Bundle_MessageSignature{MessageSignature: &protocommon.MessageSignature{
			MessageDigest: &protocommon.HashOutput{Algorithm: protocommon.HashAlgorithm_SHA2_256, Digest: ms.Digest()},
			Signature:     ms.Signature(),
		}},
	}
	return protojson.Marshal(pb)
}

// logEntry turns the kit's log entry into the bundle form: the kit keeps
// its promise private and gives no inclusion proof, which bundles v0.2+
// require. The entry is re-logged as the only leaf of the virtual log,
// with a fresh promise for that index.
func (s *Signer) logEntry(en *tlog.Entry) (*v1.TransparencyLogEntry, error) {
	src := en.TransparencyLogEntry()
	body := src.GetCanonicalizedBody()
	logID := src.GetLogId().GetKeyId()
	t := en.IntegratedTime().Unix()
	p, err := s.vs.GetInclusionProof(body)
	if err != nil {
		return nil, err
	}
	set, err := s.vs.RekorSignPayload(tlog.RekorPayload{
		Body: base64.StdEncoding.EncodeToString(body), IntegratedTime: t, LogIndex: *p.LogIndex, LogID: hex.EncodeToString(logID),
	})
	if err != nil {
		return nil, err
	}
	rootHash, err := hex.DecodeString(*p.RootHash)
	if err != nil {
		return nil, err
	}
	hashes := make([][]byte, len(p.Hashes))
	for i, h := range p.Hashes {
		if hashes[i], err = hex.DecodeString(h); err != nil {
			return nil, err
		}
	}
	return &v1.TransparencyLogEntry{
		LogIndex:         *p.LogIndex,
		LogId:            &protocommon.LogId{KeyId: logID},
		KindVersion:      &v1.KindVersion{Kind: "hashedrekord", Version: "0.0.1"},
		IntegratedTime:   t,
		InclusionPromise: &v1.InclusionPromise{SignedEntryTimestamp: set},
		InclusionProof: &v1.InclusionProof{
			LogIndex: *p.LogIndex, RootHash: rootHash, TreeSize: *p.TreeSize, Hashes: hashes,
			Checkpoint: &v1.Checkpoint{Envelope: *p.Checkpoint},
		},
		CanonicalizedBody: body,
	}, nil
}
