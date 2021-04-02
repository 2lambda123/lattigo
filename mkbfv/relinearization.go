package mkbfv

import (
	"github.com/ldsec/lattigo/v2/bfv"
	"github.com/ldsec/lattigo/v2/ring"
)

// RelinearizationWithSharedRelinKey implements the algorithm 1 in section 3.3.1 of the Chen paper
// It does relin using a precomputed shared key
func RelinearizationWithSharedRelinKey(relinKey *MKRelinearizationKey, ciphertext *MKCiphertext, params *bfv.Parameters) {

	ringQ := GetRingQ(params)
	k := uint64(len(ciphertext.peerIDs))

	res := make([]*ring.Poly, k+1)
	for i := uint64(0); i < k+1; i++ {
		res[i] = ringQ.NewPoly()
	}

	c0Prime := ciphertext.ciphertexts.Value()[0]
	cipherParts := ciphertext.ciphertexts.Value()

	for i := uint64(1); i <= k; i++ {
		ringQ.Add(cipherParts[i], cipherParts[(k+1)*i], res[i])
	}

	for i := uint64(1); i <= k; i++ {

		for j := uint64(1); j <= k; j++ {

			decomposedIJ := GInverse(cipherParts[i*(k+1)+j], params)

			c0, ci, cj := applySwitchingKey(decomposedIJ, relinKey.key[i][j], params)

			ringQ.Add(c0Prime, c0, c0Prime)
			ringQ.Add(res[i], ci, res[i])
			ringQ.Add(res[j], cj, res[j])
		}
	}

	res[0] = c0Prime
	ciphertext.ciphertexts.SetValue(res)

}

/*
// RelinearizationOnTheFly implements the algorithm 2 in section 3.3.1 of the Chen paper
// It does relin directly by linearizing each entry of the extended ciphertext and stores it in cPrime (of size k+1)
// There are (k+1)**2 ciphertexts, and k pairs of (evaluation keys Di,bi)
func RelinearizationOnTheFly(evaluationKeys []*MKEvaluationKey, publicKeys []*MKPublicKey, ciphertexts *MKCiphertext, params *bfv.Parameters) {

	ringQ := GetRingQ(params)
	k := uint64(len(evaluationKeys))
	res := make([]*ring.Poly, k+1)

	for i := uint64(0); i < k+1; i++ {
		res[i] = ringQ.NewPoly()
	}

	cipherParts := ciphertexts.ciphertexts.Value()

	c0Prime := cipherParts[0]
	tmpIJ := ringQ.NewPoly()
	tmpC0Prime := ringQ.NewPoly()
	tmpCIPrime := ringQ.NewPoly()

	for i := uint64(1); i <= k; i++ {
		ringQ.Add(cipherParts[i], cipherParts[(k+1)*i], res[i])
	}
	for i := uint64(1); i <= k; i++ {

		di0 := evaluationKeys[i-1].key[0]
		di1 := evaluationKeys[i-1].key[1]
		di2 := evaluationKeys[i-1].key[2]

		for j := uint64(1); j <= k; j++ {

			decomposedIJ := GInverse(cipherParts[i*(k+1)+j], params)

			Dot(decomposedIJ, publicKeys[j].key[0], tmpIJ, ringQ) // line 6

			decomposedTmp := GInverse(tmpIJ, params) // inverse and matric mult (line 7)

			Dot(decomposedTmp, di0, tmpC0Prime, ringQ)
			Dot(decomposedTmp, di1, tmpCIPrime, ringQ)

			ringQ.Add(c0Prime, tmpC0Prime, c0Prime)
			ringQ.Add(res[i], tmpCIPrime, res[i])

			Dot(decomposedIJ, di2, tmpIJ, ringQ) // inverse and dot product (line8)
			ringQ.Add(res[j], tmpIJ, res[j])
		}
	}
	res[0] = c0Prime
	ciphertexts.ciphertexts.SetValue(res)
}
*/

// RelinearizationOnTheFly implements the algorithm 3 in the appendix of the Chen paper
// It does relin directly by linearizing each entry of the extended ciphertext and stores it in cPrime (of size k+1)
// There are (k+1)**2 ciphertexts, and k pairs of (evaluation keys Di,bi)
func RelinearizationOnTheFly(evaluationKeys []*MKEvaluationKey, publicKeys []*MKPublicKey, ciphertexts *MKCiphertext, params *bfv.Parameters) {
	ringQP := GetRingQP(params)
	ringQ := GetRingQ(params)
	ringP := GetRingP(params)
	var baseconverter *ring.FastBasisExtender
	baseconverter = ring.NewFastBasisExtender(ringQ, ringP)
	levelQ := uint64(len(ringQ.Modulus) - 1)

	k := uint64(len(evaluationKeys))
	restmp := make([]*ring.Poly, k+1)
	res := make([]*ring.Poly, k+1)
	for i := uint64(0); i < k+1; i++ {
		restmp[i] = ringQP.NewPoly()
		res[i] = ringQ.NewPoly()
	}
	cipherParts := ciphertexts.ciphertexts.Value()
	tmpC0 := ringQP.NewPoly()
	tmpCi := ringQP.NewPoly()
	tmpIJ := ringQP.NewPoly()
	for i := uint64(1); i < k; i++ {
		di0 := evaluationKeys[i-1].key[0]
		di1 := evaluationKeys[i-1].key[1]
		di2 := evaluationKeys[i-1].key[2]
		for j := uint64(1); j < k; j++ {
			cIJDoublePrime := ringQP.NewPoly()
			decomposedIJ := GInverse(cipherParts[i*(k+1)+j], params)
			Dot(decomposedIJ, publicKeys[j].key[0], cIJDoublePrime, ringQP) // line 3
			cIJPrime := ringQ.NewPoly()
			baseconverter.ModDownPQ(levelQ, cIJDoublePrime, cIJPrime) // line 4
			decomposedTmp := GInverse(cIJPrime, params)               // inverse and matrix mult (line 5)
			Dot(decomposedTmp, di0, tmpC0, ringQP)
			Dot(decomposedTmp, di1, tmpCi, ringQP)
			ringQP.Add(cIJDoublePrime, tmpC0, cIJDoublePrime)
			ringQP.Add(restmp[i], tmpCi, restmp[i])
			Dot(decomposedIJ, di2, tmpIJ, ringQP) // line 6 of algorithm
			ringQP.Add(restmp[j], tmpIJ, restmp[j])
		}
	}
	c0Prime := ringQ.NewPoly()
	tmpModDown := ringQ.NewPoly()
	baseconverter.ModDownPQ(levelQ, restmp[0], tmpModDown)
	ringQ.Add(cipherParts[0], tmpModDown, c0Prime)
	for i := uint64(1); i <= k; i++ {
		ringQ.Add(cipherParts[i], cipherParts[(k+1)*i], res[i])
		tmpModDown := ringQ.NewPoly()
		baseconverter.ModDownPQ(levelQ, restmp[i], tmpModDown)
		ringQ.Add(res[i], tmpModDown, res[i])
	}
	res[0] = c0Prime
	ciphertexts.ciphertexts.SetValue(res)
}

// Switching keys : D0 and D1
func applySwitchingKey(decomposedCipher *MKDecomposedPoly, key *MKSwitchingKey, params *bfv.Parameters) (*ring.Poly, *ring.Poly, *ring.Poly) {

	ringQ := GetRingQ(params)
	c0 := ringQ.NewPoly()
	ci := ringQ.NewPoly()
	cj := ringQ.NewPoly()

	for i := 0; i < int(params.Beta()); i++ {
		ringQ.MulCoeffsMontgomeryAndAdd(decomposedCipher.poly[i], key.key[0].poly[i], c0)
		ringQ.MulCoeffsMontgomeryAndAdd(decomposedCipher.poly[i], key.key[1].poly[i], ci)
		ringQ.MulCoeffsMontgomeryAndAdd(decomposedCipher.poly[i], key.key[2].poly[i], cj)
	}

	return c0, ci, cj
}