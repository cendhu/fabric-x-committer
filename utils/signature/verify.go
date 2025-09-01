/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package signature

import (
	"strings"

	"github.com/cockroachdb/errors"

	"github.com/hyperledger/fabric-x-committer/api/protoblocktx"
	"github.com/hyperledger/fabric-x-common/common/cauthdsl"
	"github.com/hyperledger/fabric-x-common/common/policies"
	"github.com/hyperledger/fabric-x-common/msp"
	"github.com/hyperledger/fabric-x-common/protoutil"
)

// ThresholdVerifier verifies a digest.
type ThresholdVerifier interface {
	Verify(Digest, Signature) error
}

// NsVerifier verifies a given namespace.
type NsVerifier struct {
	thresholdVerifier ThresholdVerifier
	signatureVerifier policies.Policy
	Scheme            Scheme
	Policy            Policy
	Type              protoblocktx.PolicyType
}

// NewNsVerifier creates a new namespace verifier according to the implementation scheme.
func NewNsVerifier(p *protoblocktx.NamespacePolicy, idDeserializer msp.IdentityDeserializer) (*NsVerifier, error) {
	res := &NsVerifier{
		Scheme: strings.ToUpper(p.Scheme),
		Policy: p.Policy,
		Type:   p.Type,
	}
	var err error

	switch p.Type {
	case protoblocktx.PolicyType_THRESHOLD_RULE:
		switch res.Scheme {
		case NoScheme, "":
			res.thresholdVerifier = nil
		case Ecdsa:
			res.thresholdVerifier, err = NewEcdsaVerifier(p.Policy)
		case Bls:
			res.thresholdVerifier, err = NewBLSVerifier(p.Policy)
		case Eddsa:
			res.thresholdVerifier = &EdDSAVerifier{PublicKey: p.Policy}
		default:
			return nil, errors.Newf("scheme '%v' not supported", p.Scheme)
		}
	case protoblocktx.PolicyType_SIGNATURE_RULE:
		pp := cauthdsl.NewPolicyProvider(idDeserializer)
		res.signatureVerifier, _, err = pp.NewPolicy(p.Policy)
	default:
		return nil, errors.Newf("policy type '%v' not supported", p.Type)
	}

	return res, errors.Wrap(err, "failed creating verifier")
}

// VerifyNs verifies a transaction's namespace signature.
func (v *NsVerifier) VerifyNs(txID string, tx *protoblocktx.Tx, nsIndex int) error {
	if nsIndex < 0 || nsIndex >= len(tx.Namespaces) || nsIndex >= len(tx.SignatureSets) {
		return errors.New("namespace index out of range")
	}

	switch v.Type {
	case protoblocktx.PolicyType_THRESHOLD_RULE:
		if v.thresholdVerifier == nil {
			return nil
		}
		digest, err := DigestTxNamespace(txID, tx.Namespaces[nsIndex])
		if err != nil {
			return err
		}
		return v.thresholdVerifier.Verify(digest, tx.SignatureSets[nsIndex].SignaturesWithIdentity[0].Signature)
	case protoblocktx.PolicyType_SIGNATURE_RULE:
		signatureSet := []*protoutil.SignedData{}
		return v.signatureVerifier.EvaluateSignedData(signatureSet)
	default:
		return errors.Newf("policy type [%v] not supported", v.Type)
	}
}
