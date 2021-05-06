package ckks

import (
	"github.com/ldsec/lattigo/v2/ring"
	"github.com/ldsec/lattigo/v2/rlwe"
	"github.com/ldsec/lattigo/v2/utils"

	//"log"
	"math"
	//"time"
)

// Bootstrapp re-encrypt a ciphertext at lvl Q0 to a ciphertext at MaxLevel-k where k is the depth of the bootstrapping circuit.
// If the input ciphertext level is zero, the input scale must be an exact power of two smaller or equal to round(Q0/2^{10}).
// If the input ciphertext is at level one or more, the input scale does not need to be an exact power of two as one level
// can be used to do a scale matching.
func (btp *Bootstrapper) Bootstrapp(ct *Ciphertext) *Ciphertext {
	//var t time.Time
	var ct0, ct1 *Ciphertext

	// Drops the level to 1
	for ct.Level() > 1 {
		btp.evaluator.DropLevel(ct, 1)
	}

	// Brings the ciphertext scale to Q0/2^{10}
	if ct.Level() == 1 {

		// if one level is available, then uses it to match the scale
		btp.evaluator.SetScale(ct, btp.prescale)

		// then drops to level 0
		for ct.Level() != 0 {
			btp.evaluator.DropLevel(ct, 1)
		}

	} else {

		// else drop to level 0
		for ct.Level() != 0 {
			btp.evaluator.DropLevel(ct, 1)
		}

		// and does an integer constant mult by round((Q0/2^{10})/ctscle)
		btp.evaluator.ScaleUp(ct, math.Round(btp.prescale/ct.Scale()), ct)
	}

	// ModUp ct_{Q_0} -> ct_{Q_L}
	//t = time.Now()
	ct = btp.modUp(ct)
	//log.Println("After ModUp  :", time.Now().Sub(t), ct.Level(), ct.Scale())

	// Brings the ciphertext scale to sineQi/(Q0/scale) if its under
	btp.evaluator.ScaleUp(ct, math.Round(btp.postscale/ct.Scale()), ct)

	//SubSum X -> (N/dslots) * Y^dslots
	//t = time.Now()
	ct = btp.subSum(ct)
	//log.Println("After SubSum :", time.Now().Sub(t), ct.Level(), ct.Scale())
	// Part 1 : Coeffs to slots

	//t = time.Now()
	ct0, ct1 = btp.coeffsToSlots(ct)
	//log.Println("After CtS    :", time.Now().Sub(t), ct0.Level(), ct0.Scale())

	// Part 2 : SineEval
	//t = time.Now()
	ct0, ct1 = btp.evaluateSine(ct0, ct1)
	//log.Println("After Sine   :", time.Now().Sub(t), ct0.Level(), ct0.Scale())

	// Part 3 : Slots to coeffs
	//t = time.Now()
	ct0 = btp.slotsToCoeffs(ct0, ct1)

	ct0.SetScale(math.Exp2(math.Round(math.Log2(ct0.Scale())))) // rounds to the nearest power of two
	//log.Println("After StC    :", time.Now().Sub(t), ct0.Level(), ct0.Scale())
	return ct0
}

func (btp *Bootstrapper) subSum(ct *Ciphertext) *Ciphertext {

	for i := btp.params.LogSlots(); i < btp.params.MaxLogSlots(); i++ {

		btp.evaluator.Rotate(ct, 1<<i, btp.ctxpool)

		btp.evaluator.Add(ct, btp.ctxpool, ct)
	}

	return ct
}

func (btp *Bootstrapper) modUp(ct *Ciphertext) *Ciphertext {

	ringQ := btp.evaluator.ringQ

	ct.InvNTT(ringQ, &ct.Element.Element)

	// Extend the ciphertext with zero polynomials.
	for u := range ct.Value {
		ct.Value[u].Coeffs = append(ct.Value[u].Coeffs, make([][]uint64, btp.params.MaxLevel())...)
		for i := uint64(1); i < btp.params.MaxLevel()+1; i++ {
			ct.Value[u].Coeffs[i] = make([]uint64, btp.params.N())
		}
	}

	//Centers the values around Q0 and extends the basis from Q0 to QL
	Q := ringQ.Modulus[0]
	bredparams := ringQ.GetBredParams()

	var coeff, qi uint64
	for u := range ct.Value {

		for j := uint64(0); j < btp.params.N(); j++ {

			coeff = ct.Value[u].Coeffs[0][j]

			for i := uint64(1); i < btp.params.MaxLevel()+1; i++ {

				qi = ringQ.Modulus[i]

				if coeff > (Q >> 1) {
					ct.Value[u].Coeffs[i][j] = qi - ring.BRedAdd(Q-coeff, qi, bredparams[i])
				} else {
					ct.Value[u].Coeffs[i][j] = ring.BRedAdd(coeff, qi, bredparams[i])
				}
			}
		}
	}

	ct.NTT(ringQ, &ct.Element.Element)

	return ct
}

func (btp *Bootstrapper) coeffsToSlots(vec *Ciphertext) (ct0, ct1 *Ciphertext) {

	var zV, zVconj *Ciphertext

	zV = btp.dft(vec, btp.pDFTInv, true)

	// Extraction of real and imaginary parts.
	zVconj = btp.ConjugateNew(zV)

	// The real part is stored in ct0
	ct0 = btp.AddNew(zV, zVconj)

	// The imaginary part is stored in ct1
	ct1 = btp.SubNew(zV, zVconj)

	btp.DivByi(ct1, ct1)

	// If repacking, then ct0 and ct1 right n/2 slots are zero.
	if btp.repack {

		// The imaginary part is put in the right n/2 slots of ct0.
		btp.Rotate(ct1, int(btp.params.Slots()), ct1)

		btp.Add(ct0, ct1, ct0)

		return ct0, nil
	}

	zV = nil
	zVconj = nil

	return ct0, ct1
}

func (btp *Bootstrapper) slotsToCoeffs(ct0, ct1 *Ciphertext) (ct *Ciphertext) {

	// If full packing, the repacking can be done directly using ct0 and ct1.
	if !btp.repack {

		btp.MultByi(ct1, ct1)

		btp.Add(ct0, ct1, ct0)
	}

	ct1 = nil

	return btp.dft(ct0, btp.pDFT, false)
}

func (btp *Bootstrapper) dft(vec *Ciphertext, plainVectors []*dftvectors, forward bool) *Ciphertext {

	// Sequentially multiplies w with the provided dft matrices.
	for _, plainVector := range plainVectors {
		vec = btp.multiplyByDiagMatrice(vec, plainVector)
		if err := btp.Rescale(vec, btp.scale, vec); err != nil {
			panic(err)
		}
	}

	return vec
}

func (btp *Bootstrapper) multiplyByDiagMatrice(vec *Ciphertext, plainVectors *dftvectors) (res *Ciphertext) {

	ringQ := btp.ringQ
	ringP := btp.ringP
	levelQ := vec.Level()
	levelP := btp.params.PCount() - 1

	var N1 uint64

	res = NewCiphertext(btp.params, 1, vec.Level(), vec.Scale())

	// N1*N2 = N
	N1 = plainVectors.N1

	// Computes the rotations indexes of the non-zero rows of the diagonalized DFT matrix for the baby-step giant-step algorithm
	index := make(map[uint64][]uint64)
	rotations := []uint64{}
	for key := range plainVectors.Vec {

		idx1 := key / N1
		idx2 := key & (N1 - 1)

		if index[idx1] == nil {
			index[idx1] = []uint64{idx2}
		} else {
			index[idx1] = append(index[idx1], idx2)
		}

		if !utils.IsInSliceUint64(idx2, rotations) {
			rotations = append(rotations, idx2)
		}
	}

	// Pre-rotates ciphertext for the baby-step giant-step algorithm, does not divide by P yet
	vecRotQ, vecRotP := btp.rotateHoistedNoModDown(vec, rotations, btp.Rtks)

	// Accumulator inner loop
	tmpQ0 := btp.evaluator.poolQMul[0] // unused memory pool from evaluator
	tmpQ1 := btp.evaluator.poolQMul[1] // unused memory pool from evaluator

	// Accumulator outer loop
	tmpQ2 := btp.evaluator.poolQMul[2] // unused memory pool from evaluator
	tmpQ3 := btp.poolQ[0]
	tmpP2 := btp.poolP[0]
	tmpP3 := btp.poolP[1]

	// Keyswitch accumulator
	pool2Q := btp.evaluator.poolQ[1] // res(c0', c1') from evaluator keyswitch memory pool
	pool3Q := btp.evaluator.poolQ[2] // res(c0', c1') from evaluator keyswitch memory pool
	pool2P := btp.evaluator.poolP[1] // res(c0', c1') from evaluator keyswitch memory pool
	pool3P := btp.evaluator.poolP[2] // res(c0', c1') from evaluator keyswitch memory pool

	N1Rot := 0
	N2Rot := 0

	c0 := vec.Value[0].CopyNew()

	ringQ.MulScalarBigintLvl(levelQ, c0, ringP.ModulusBigint, c0) // P*c0

	for _, i := range rotations {
		if i != 0 {
			galEl := btp.params.GaloisElementForColumnRotationBy(int(i))
			ring.PermuteNTTWithIndexLvl(levelQ, c0, btp.permuteNTTIndex[galEl], tmpQ0) // phi(P*c0)
			ringQ.AddLvl(levelQ, vecRotQ[i][0], tmpQ0, vecRotQ[i][0])                  // phi(d0_Q) += phi(P*c0)
		}
	}

	// OUTER LOOP
	cnt0 := 0
	for j := range index {

		if j != 0 {

			// INNER LOOP
			state := false
			cnt1 := 0
			for _, i := range index[j] {

				if i == 0 {
					state = true
				} else {

					N1Rot++

					plaintextQ := plainVectors.Vec[N1*j+uint64(i)][0]
					plaintextP := plainVectors.Vec[N1*j+uint64(i)][1]

					if cnt1 == 0 {
						ringQ.MulCoeffsMontgomeryLvl(levelQ, plaintextQ, vecRotQ[i][0], tmpQ0) // phi(P*c0 + d0_Q) * plaintext
						ringQ.MulCoeffsMontgomeryLvl(levelQ, plaintextQ, vecRotQ[i][1], tmpQ1) // phi(d1_Q) * plaintext
						ringP.MulCoeffsMontgomery(plaintextP, vecRotP[i][0], pool2P)           // phi(d0_P) * plaintext
						ringP.MulCoeffsMontgomery(plaintextP, vecRotP[i][1], pool3P)           // phi(d1_P) * plaintext
					} else {
						ringQ.MulCoeffsMontgomeryAndAddLvl(levelQ, plaintextQ, vecRotQ[i][0], tmpQ0) // phi(d0_Q) * plaintext
						ringQ.MulCoeffsMontgomeryAndAddLvl(levelQ, plaintextQ, vecRotQ[i][1], tmpQ1) // phi(d1_Q) * plaintext
						ringP.MulCoeffsMontgomeryAndAdd(plaintextP, vecRotP[i][0], pool2P)           // phi(d0_P) * plaintext
						ringP.MulCoeffsMontgomeryAndAdd(plaintextP, vecRotP[i][1], pool3P)           // phi(d1_P) * plaintext
					}

					cnt1++
				}
			}

			// Hoisting of the ModDown of sum(sum(phi(d0 + P*c0) * plaintext)) and sum(sum(phi(d1) * plaintext))
			btp.baseconverter.ModDownSplitNTTPQ(levelQ, tmpQ0, pool2P, tmpQ0) // sum(phi(d0) * plaintext)/P
			btp.baseconverter.ModDownSplitNTTPQ(levelQ, tmpQ1, pool3P, tmpQ1) // sum(phi(d1) * plaintext)/P

			// If i == 0
			if state {
				N1Rot++
				ringQ.MulCoeffsMontgomeryAndAddLvl(levelQ, plainVectors.Vec[N1*j][0], vec.Value[0], tmpQ0) // c0 * plaintext + sum(phi(d0) * plaintext)/P + phi(c0) * plaintext mod Q
				ringQ.MulCoeffsMontgomeryAndAddLvl(levelQ, plainVectors.Vec[N1*j][0], vec.Value[1], tmpQ1) // c1 * plaintext + sum(phi(d1) * plaintext)/P + phi(c1) * plaintext mod Q
			}

			galEl := btp.params.GaloisElementForColumnRotationBy(int(N1 * j))

			btp.switchKeysInPlaceNoModDown(levelQ, tmpQ1, btp.Rtks.Keys[galEl], pool2Q, pool2P, pool3Q, pool3P) // Switchkey(phi(tmpRes_1)) = (d0, d1) in base QP

			// Outer loop rotations
			ring.PermuteNTTWithIndexLvl(levelQ, tmpQ0, btp.permuteNTTIndex[galEl], tmpQ1) // phi(tmpRes_0)
			ringQ.AddLvl(levelQ, res.Value[0], tmpQ1, res.Value[0])                       // res += phi(tmpRes)

			rot := btp.permuteNTTIndex[galEl]

			N2Rot++

			if cnt0 == 0 {
				ring.PermuteNTTWithIndexLvl(levelQ, pool2Q, rot, tmpQ2) // sum(phi(d0_Q))
				ring.PermuteNTTWithIndexLvl(levelQ, pool3Q, rot, tmpQ3) // sum(phi(d1_Q))
				ring.PermuteNTTWithIndexLvl(levelP, pool2P, rot, tmpP2) // sum(phi(d0_P))
				ring.PermuteNTTWithIndexLvl(levelP, pool3P, rot, tmpP3) // sum(phi(d1_P))
			} else {
				ring.PermuteNTTWithIndexAndAddNoModLvl(levelQ, pool2Q, rot, tmpQ2) // sum(phi(d0_Q))
				ring.PermuteNTTWithIndexAndAddNoModLvl(levelQ, pool3Q, rot, tmpQ3) // sum(phi(d1_Q))
				ring.PermuteNTTWithIndexAndAddNoModLvl(levelP, pool2P, rot, tmpP2) // sum(phi(d0_P))
				ring.PermuteNTTWithIndexAndAddNoModLvl(levelP, pool3P, rot, tmpP3) // sum(phi(d1_P))
			}

			if cnt0 == 7 {
				ringQ.ReduceLvl(levelQ, tmpQ2, tmpQ2)
				ringQ.ReduceLvl(levelQ, tmpQ3, tmpQ3)
				ringP.Reduce(tmpP2, tmpP2)
				ringP.Reduce(tmpP3, tmpP3)
			}

			cnt0++
		}
	}

	if cnt0 != 7 {
		ringQ.ReduceLvl(levelQ, tmpQ2, tmpQ2)
		ringQ.ReduceLvl(levelQ, tmpQ3, tmpQ3)
		ringP.Reduce(tmpP2, tmpP2)
		ringP.Reduce(tmpP3, tmpP3)
	}

	// if j == 0 (N2 rotation by zero)
	state := false
	for _, i := range index[0] {

		if i == 0 {
			state = true
		} else {

			plaintextQ := plainVectors.Vec[uint64(i)][0]
			plaintextP := plainVectors.Vec[uint64(i)][1]
			N1Rot++
			// keyswitch(c1_Q) = (d0_QP, d1_QP)
			ringQ.MulCoeffsMontgomeryAndAddLvl(levelQ, plaintextQ, vecRotQ[i][0], tmpQ2) // phi(P*c0 + d0_Q) * plaintext
			ringQ.MulCoeffsMontgomeryAndAddLvl(levelQ, plaintextQ, vecRotQ[i][1], tmpQ3) // phi(d1_Q) * plaintext
			ringP.MulCoeffsMontgomeryAndAdd(plaintextP, vecRotP[i][0], tmpP2)            // phi(d0_P) * plaintext
			ringP.MulCoeffsMontgomeryAndAdd(plaintextP, vecRotP[i][1], tmpP3)            // phi(d1_P) * plaintext
		}
	}

	btp.baseconverter.ModDownSplitNTTPQ(levelQ, tmpQ2, tmpP2, tmpQ2) // sum(phi(c0 * P + d0_QP))/P
	btp.baseconverter.ModDownSplitNTTPQ(levelQ, tmpQ3, tmpP3, tmpQ3) // sum(phi(d1_QP))/P

	ringQ.AddLvl(levelQ, res.Value[0], tmpQ2, res.Value[0]) // res += sum(phi(c0 * P + d0_QP))/P
	ringQ.AddLvl(levelQ, res.Value[1], tmpQ3, res.Value[1]) // res += sum(phi(d1_QP))/P

	if state { // Rotation by zero
		N1Rot++
		ringQ.MulCoeffsMontgomeryAndAddLvl(levelQ, plainVectors.Vec[0][0], vec.Value[0], res.Value[0]) // res += c0_Q * plaintext
		ringQ.MulCoeffsMontgomeryAndAddLvl(levelQ, plainVectors.Vec[0][0], vec.Value[1], res.Value[1]) // res += c1_Q * plaintext
	}

	res.SetScale(plainVectors.Scale * vec.Scale())

	vecRotQ, vecRotP, c0 = nil, nil, nil

	//log.Println(N1Rot, N2Rot)

	return
}

// RotateHoisted takes an input Ciphertext and a list of rotations and returns a map of Ciphertext, where each element of the map is the input Ciphertext
// rotation by one element of the list. It is much faster than sequential calls to RotateColumns.
func (eval *evaluator) rotateHoistedNoModDown(ct0 *Ciphertext, rotations []uint64, rotkeys *rlwe.RotationKeySet) (cOutQ, cOutP map[uint64][2]*ring.Poly) {

	// Pre-computation for rotations using hoisting

	c2NTT := ct0.Value[1]
	c2InvNTT := eval.ringQ.NewPoly() // IMPROVEMENT: maybe have a pre-allocated memory pool ?
	eval.ringQ.InvNTTLvl(ct0.Level(), c2NTT, c2InvNTT)

	alpha := eval.params.Alpha()
	beta := uint64(math.Ceil(float64(ct0.Level()+1) / float64(alpha)))

	// IMPROVEMENT: maybe have a pre-allocated memory pool ?
	c2QiQDecomp := make([]*ring.Poly, beta)
	c2QiPDecomp := make([]*ring.Poly, beta)

	for i := uint64(0); i < beta; i++ {
		c2QiQDecomp[i] = eval.ringQ.NewPoly()
		c2QiPDecomp[i] = eval.ringP.NewPoly()
		eval.decomposeAndSplitNTT(ct0.Level(), i, c2NTT, c2InvNTT, c2QiQDecomp[i], c2QiPDecomp[i])
	}

	c2InvNTT = nil

	cOutQ = make(map[uint64][2]*ring.Poly)
	cOutP = make(map[uint64][2]*ring.Poly)
	for _, i := range rotations {

		i &= ((eval.ringQ.N >> 1) - 1)

		if i != 0 {
			cOutQ[i] = [2]*ring.Poly{eval.ringQ.NewPolyLvl(ct0.Level()), eval.ringQ.NewPolyLvl(ct0.Level())}
			cOutP[i] = [2]*ring.Poly{eval.ringP.NewPoly(), eval.ringP.NewPoly()}
			eval.permuteNTTHoistedNoModDown(ct0, c2QiQDecomp, c2QiPDecomp, i, rotkeys, cOutQ[i], cOutP[i])
		}
	}

	c2QiQDecomp = nil
	c2QiPDecomp = nil

	return
}

func (eval *evaluator) permuteNTTHoistedNoModDown(ct0 *Ciphertext, c2QiQDecomp, c2QiPDecomp []*ring.Poly, k uint64, rotKeys *rlwe.RotationKeySet, ctOutQ, ctOutP [2]*ring.Poly) {

	pool2Q := eval.poolQ[0]
	pool3Q := eval.poolQ[1]

	pool2P := eval.poolP[0]
	pool3P := eval.poolP[1]

	levelQ := ct0.Level()
	levelP := eval.params.PCount() - 1

	galEl := eval.params.GaloisElementForColumnRotationBy(int(k))
	rtk := rotKeys.Keys[galEl]
	indexes := eval.permuteNTTIndex[galEl]

	eval.keyswitchHoistedNoModDown(levelQ, c2QiQDecomp, c2QiPDecomp, rtk, pool2Q, pool3Q, pool2P, pool3P)

	ring.PermuteNTTWithIndexLvl(levelQ, pool2Q, indexes, ctOutQ[0])
	ring.PermuteNTTWithIndexLvl(levelQ, pool3Q, indexes, ctOutQ[1])

	ring.PermuteNTTWithIndexLvl(levelP, pool2P, indexes, ctOutP[0])
	ring.PermuteNTTWithIndexLvl(levelP, pool3P, indexes, ctOutP[1])
}

// Sine Evaluation ct0 = Q/(2pi) * sin((2pi/Q) * ct0)
func (btp *Bootstrapper) evaluateSine(ct0, ct1 *Ciphertext) (*Ciphertext, *Ciphertext) {

	ct0.MulScale(btp.deviation)
	btp.scale = ct0.Scale() // Reference scale is changed to the new ciphertext's scale.

	// pre-computes the target scale for the output of the polynomial evaluation such that
	// the output scale after the polynomial evaluation followed by the double angle formula
	// does not change the scale of the ciphertext.
	for i := uint64(0); i < btp.SinRescal; i++ {
		btp.scale *= float64(btp.params.Q()[btp.StCLevel[0]+i+1])
		btp.scale = math.Sqrt(btp.scale)
	}

	ct0 = btp.evaluateCheby(ct0)

	ct0.DivScale(btp.deviation * btp.postscale / btp.params.Scale())

	if ct1 != nil {
		ct1.MulScale(btp.deviation)
		ct1 = btp.evaluateCheby(ct1)
		ct1.DivScale(btp.deviation * btp.postscale / btp.params.Scale())
	}

	// Reference scale is changed back to the current ciphertext's scale.
	btp.scale = ct0.Scale()

	return ct0, ct1
}

func (btp *Bootstrapper) evaluateCheby(ct *Ciphertext) *Ciphertext {

	cheby := btp.chebycoeffs

	sqrt2pi := math.Pow(0.15915494309189535, 1.0/float64(int(1<<btp.SinRescal)))

	if btp.SinType == Cos1 || btp.SinType == Cos2 {
		scfac := complex(float64(int(1<<btp.SinRescal)), 0)
		btp.AddConst(ct, -0.5/(scfac*(cheby.b-cheby.a)), ct)
	}

	ct, _ = btp.EvaluateCheby(ct, cheby)

	for i := uint64(0); i < btp.SinRescal; i++ {
		sqrt2pi *= sqrt2pi
		btp.MulRelin(ct, ct, ct)
		btp.Add(ct, ct, ct)
		btp.AddConst(ct, -sqrt2pi, ct)
		if err := btp.Rescale(ct, btp.scale, ct); err != nil {
			panic(err)
		}
	}

	return ct
}
