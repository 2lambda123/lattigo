package he

import (
	"github.com/tuneinsight/lattigo/v4/ring"
	"github.com/tuneinsight/lattigo/v4/rlwe"

	"github.com/tuneinsight/lattigo/v4/rlwe/ringqp"
)

// EncoderInterface defines a set of common and scheme agnostic method provided by an Encoder struct.
type EncoderInterface[T any, U *ring.Poly | ringqp.Poly | *rlwe.Plaintext] interface {
	Encode(values []T, metaData *rlwe.MetaData, output U) (err error)
	Parameters() rlwe.ParametersInterface
}