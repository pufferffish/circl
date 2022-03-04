package oprf

import (
	"crypto/rand"

	"github.com/cloudflare/circl/group"
	"github.com/cloudflare/circl/group/dleq"
)

type client struct{ params }

type Client struct {
	client
}

type VerifiableClient struct {
	client
	pkS *PublicKey
}

type PartialObliviousClient struct {
	client
	pkS *PublicKey
}

func (c client) Blind(inputs [][]byte) (*FinalizeData, *EvaluationRequest, error) {
	if len(inputs) == 0 {
		return nil, nil, ErrInvalidInput
	}

	blinds := make([]blind, len(inputs))
	for i := range inputs {
		blinds[i] = c.params.Group.RandomScalar(rand.Reader)
	}

	return c.blind(inputs, blinds)
}

func (c client) DeterministicBlind(inputs, blinds [][]byte) (*FinalizeData, *EvaluationRequest, error) {
	if len(inputs) == 0 {
		return nil, nil, ErrInvalidInput
	}
	if len(inputs) != len(blinds) {
		return nil, nil, ErrInvalidInput
	}

	blindScalars := make([]blind, len(blinds))
	for i := range blinds {
		blindScalars[i] = c.params.g.NewScalar()
		err := blindScalars[i].UnmarshalBinary(blinds[i])
		if err != nil {
			return nil, nil, ErrInvalidInput
		}
	}

	return c.blind(inputs, blindScalars)
}

func (c client) blind(inputs [][]byte, blinds []blind) (*FinalizeData, *EvaluationRequest, error) {
	blindedElements := make([]Blinded, len(inputs))
	dst := c.params.getDST(hashToGroupDST)
	for i := range inputs {
		point := c.params.Group.HashToElement(inputs[i], dst)
		if point.IsIdentity() {
			return nil, nil, ErrInvalidInput
		}
		blindedElements[i] = c.params.Group.NewElement().Mul(point, blinds[i])
	}

	evalReq := &EvaluationRequest{blindedElements}
	finData := &FinalizeData{inputs, blinds, evalReq}

	return finData, evalReq, nil
}

func (c client) unblind(serUnblindeds [][]byte, blindeds []group.Element, blind []blind) error {
	var err error
	invBlind := c.params.Group.NewScalar()
	U := c.params.Group.NewElement()

	for i := range blindeds {
		invBlind.Inv(blind[i])
		U.Mul(blindeds[i], invBlind)
		serUnblindeds[i], err = U.MarshalBinaryCompress()
		if err != nil {
			return err
		}
	}

	return nil
}

func (c client) validate(f *FinalizeData, e *Evaluation) (err error) {
	if l := len(f.blinds); len(f.evalReq.Elements) != l || len(e.Elements) != l {
		err = ErrInvalidInput
	}

	return
}

func (c client) finalize(f *FinalizeData, e *Evaluation, info []byte) ([][]byte, error) {
	unblindedElements := make([][]byte, len(f.blinds))
	err := c.unblind(unblindedElements, e.Elements, f.blinds)
	if err != nil {
		return nil, err
	}

	h := c.params.Hash.New()
	outputs := make([][]byte, len(f.inputs))
	for i := range f.inputs {
		outputs[i] = c.params.finalizeHash(h, f.inputs[i], info, unblindedElements[i])
	}

	return outputs, nil
}

func (c Client) Finalize(f *FinalizeData, e *Evaluation) (outputs [][]byte, err error) {
	if err = c.validate(f, e); err != nil {
		return nil, err
	}

	return c.client.finalize(f, e, nil)
}

func (c VerifiableClient) Finalize(f *FinalizeData, e *Evaluation) (outputs [][]byte, err error) {
	if err := c.validate(f, e); err != nil {
		return nil, err
	}

	if !(dleq.Verifier{Params: c.getDLEQParams()}).VerifyBatch(
		c.params.Group.Generator(),
		c.pkS.e,
		f.evalReq.Elements,
		e.Elements,
		e.Proof,
	) {
		return nil, ErrInvalidProof
	}

	return c.client.finalize(f, e, nil)
}

func (c PartialObliviousClient) Finalize(f *FinalizeData, e *Evaluation, info []byte) (outputs [][]byte, err error) {
	if err = c.validate(f, e); err != nil {
		return nil, err
	}

	tweakedKey, err := c.pointFromInfo(info)
	if err != nil {
		return nil, err
	}

	if !(dleq.Verifier{Params: c.getDLEQParams()}).VerifyBatch(
		c.params.Group.Generator(),
		tweakedKey,
		e.Elements,
		f.evalReq.Elements,
		e.Proof,
	) {
		return nil, ErrInvalidProof
	}

	return c.client.finalize(f, e, info)
}

func (c PartialObliviousClient) pointFromInfo(info []byte) (group.Element, error) {
	m, err := c.params.scalarFromInfo(info)
	if err != nil {
		return nil, err
	}

	T := c.params.Group.NewElement().MulGen(m)
	tweakedKey := c.params.Group.NewElement().Add(T, c.pkS.e)
	if tweakedKey.IsIdentity() {
		return nil, ErrInvalidInfo
	}

	return tweakedKey, nil
}
