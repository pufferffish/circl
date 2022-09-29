package mceliece348864

import (
	"bytes"
	cryptoRand "crypto/rand"

	"github.com/cloudflare/circl/internal/sha3"
	"github.com/cloudflare/circl/kem"
	"github.com/cloudflare/circl/kem/mceliece"
	"github.com/cloudflare/circl/math/gf4096"
)

const (
	sysT                  = 64 // F(y) is 64 degree
	gfBits                = gf4096.GfBits
	gfMask                = gf4096.GfMask
	unusedBits            = 16 - gfBits
	sysN                  = 3488
	condBytes             = (1 << (gfBits - 4)) * (2*gfBits - 1)
	irrBytes              = sysT * 2
	pkNRows               = sysT * gfBits
	pkNCols               = sysN - pkNRows
	pkRowBytes            = (pkNCols + 7) / 8
	syndBytes             = (pkNRows + 7) / 8
	PublicKeySize         = 261120
	PrivateKeySize        = 6492
	CryptoCiphertextBytes = 128
	seedSize              = 32
)

type PublicKey struct {
	pk [PublicKeySize]byte
}

type PrivateKey struct {
	sk [PrivateKeySize]byte
}

type gf = gf4096.Gf

func deriveKeyPair(entropy []byte) (*PublicKey, *PrivateKey) {
	const (
		irrPolys  = sysN/8 + (1<<gfBits)*4
		seedIndex = sysN/8 + (1<<gfBits)*4 + sysT*2
		permIndex = sysN / 8
		sBase     = 32 + 8 + irrBytes + condBytes
	)

	var (
		pk [PublicKeySize]byte
		sk [PrivateKeySize]byte
	)

	seed := [33]byte{64}
	r := [sysN/8 + (1<<gfBits)*4 + sysT*2 + 32]byte{}

	f := [sysT]gf{}
	irr := [sysT]gf{}
	perm := [1 << gfBits]uint32{}
	pi := [1 << gfBits]int16{}
	pivots := uint64(0xFFFFFFFF)

	copy(seed[1:], entropy[:])

	for {
		// expanding and updating the seed
		err := shake256(r[:], seed[0:33])
		if err != nil {
			panic(err)
		}

		copy(sk[:32], seed[1:])
		copy(seed[1:], r[len(r)-32:])

		temp := r[irrPolys:seedIndex]
		for i := 0; i < sysT; i++ {
			f[i] = loadGf(temp)
			temp = temp[2:]
		}

		if !minimalPolynomial(&irr, &f) {
			continue
		}

		temp = sk[40 : 40+irrBytes]
		for i := 0; i < sysT; i++ {
			storeGf(temp, irr[i])
			temp = temp[2:]
		}

		// generating permutation
		temp = r[permIndex:irrPolys]
		for i := 0; i < 1<<gfBits; i++ {
			perm[i] = load4(temp)
			temp = temp[4:]
		}

		if !pkGen(&pk, sk[40:40+irrBytes], &perm, &pi, pivots) {
			continue
		}

		mceliece.ControlBitsFromPermutation(sk[32+8+irrBytes:], pi[:], gfBits, 1<<gfBits)
		copy(sk[sBase:sBase+sysN/8], r[0:sysN/8])
		store8(sk[32:40], pivots)
		return &PublicKey{pk: pk}, &PrivateKey{sk: sk}
	}
}

// check if element is 0, returns a mask with all bits set if so, and 0 otherwise
func isZeroMask(element gf) uint16 {
	t := uint32(element) - 1
	t >>= 19
	return uint16(t)
}

// calculate the minimal polynomial of f and store it in out
func minimalPolynomial(out *[sysT]gf, f *[sysT]gf) bool {
	mat := [sysT + 1][sysT]gf{}
	mat[0][0] = 1
	for i := 1; i < sysT; i++ {
		mat[0][i] = 0
	}

	for i := 0; i < sysT; i++ {
		mat[1][i] = f[i]
	}

	for i := 2; i <= sysT; i++ {
		polyMul(&mat[i], &mat[i-1], f)
	}

	for j := 0; j < sysT; j++ {
		for k := j + 1; k < sysT; k++ {
			mask := isZeroMask(mat[j][j])
			// if mat[j][j] is not zero, add mat[c..sysT+1][k] to mat[c][j]
			// do nothing otherwise
			for c := j; c <= sysT; c++ {
				mat[c][j] ^= mat[c][k] & mask
			}
		}

		if mat[j][j] == 0 {
			return false
		}

		inv := gf4096.Inv(mat[j][j])
		for c := 0; c <= sysT; c++ {
			mat[c][j] = gf4096.Mul(mat[c][j], inv)
		}

		for k := 0; k < sysT; k++ {
			if k != j {
				t := mat[j][k]
				for c := 0; c <= sysT; c++ {
					mat[c][k] ^= gf4096.Mul(mat[c][j], t)
				}
			}
		}
	}

	for i := 0; i < sysT; i++ {
		out[i] = mat[sysT][i]
	}

	return true
}

// calculate the product of a and b in Fq^t
func polyMul(out *[sysT]gf, a *[sysT]gf, b *[sysT]gf) {
	product := [sysT*2 - 1]gf{}
	for i := 0; i < sysT; i++ {
		for j := 0; j < sysT; j++ {
			product[i+j] ^= gf4096.Mul(a[i], b[j])
		}
	}

	for i := (sysT - 1) * 2; i >= sysT; i-- {
		// polynomial reduction
		product[i-sysT+3] ^= product[i]
		product[i-sysT+1] ^= product[i]
		product[i-sysT] ^= gf4096.Mul(product[i], 2)
	}

	for i := 0; i < sysT; i++ {
		out[i] = product[i]
	}
}

// nolint:unparam
func pkGen(pk *[pkNRows * pkRowBytes]byte, sk []byte, perm *[1 << gfBits]uint32, pi *[1 << gfBits]int16, pivots uint64) bool {
	buf := [1 << gfBits]uint64{}
	mat := [pkNRows][sysN / 8]byte{}
	g := [sysT + 1]gf{}
	L := [sysN]gf{}
	inv := [sysN]gf{}

	g[sysT] = 1
	for i := 0; i < sysT; i++ {
		g[i] = loadGf(sk)
		sk = sk[2:]
	}

	for i := 0; i < 1<<gfBits; i++ {
		buf[i] = uint64(perm[i])
		buf[i] <<= 31
		buf[i] |= uint64(i)
	}

	mceliece.UInt64Sort(buf[:], 1<<gfBits)

	for i := 1; i < (1 << gfBits); i++ {
		if (buf[i-1] >> 31) == (buf[i] >> 31) {
			return false
		}
	}

	for i := 0; i < (1 << gfBits); i++ {
		pi[i] = int16(buf[i] & gfMask)
	}

	for i := 0; i < sysN; i++ {
		L[i] = bitRev(gf(pi[i]))
	}

	// filling the matrix
	root(&inv, &g, &L)

	for i := 0; i < sysN; i++ {
		inv[i] = gf4096.Inv(inv[i])
	}

	for i := 0; i < sysT; i++ {
		for j := 0; j < sysN; j += 8 {
			for k := 0; k < gfBits; k++ {
				b := byte(inv[j+7]>>k) & 1
				b <<= 1
				b |= byte(inv[j+6]>>k) & 1
				b <<= 1
				b |= byte(inv[j+5]>>k) & 1
				b <<= 1
				b |= byte(inv[j+4]>>k) & 1
				b <<= 1
				b |= byte(inv[j+3]>>k) & 1
				b <<= 1
				b |= byte(inv[j+2]>>k) & 1
				b <<= 1
				b |= byte(inv[j+1]>>k) & 1
				b <<= 1
				b |= byte(inv[j+0]>>k) & 1

				mat[i*gfBits+k][j/8] = b
			}
		}

		for j := 0; j < sysN; j++ {
			inv[j] = gf4096.Mul(inv[j], L[j])
		}
	}

	// gaussian elimination
	for i := 0; i < (pkNRows+7)/8; i++ {
		for j := 0; j < 8; j++ {
			row := i*8 + j

			if row >= pkNRows {
				break
			}

			for k := row + 1; k < pkNRows; k++ {
				mask := mat[row][i] ^ mat[k][i]
				mask >>= j
				mask &= 1
				mask = -mask

				for c := 0; c < sysN/8; c++ {
					mat[row][c] ^= mat[k][c] & mask
				}
			}

			// return if not systematic
			if ((mat[row][i] >> j) & 1) == 0 {
				return false
			}

			for k := 0; k < pkNRows; k++ {
				if k != row {
					mask := mat[k][i] >> j
					mask &= 1
					mask = -mask

					for c := 0; c < sysN/8; c++ {
						mat[k][c] ^= mat[row][c] & mask
					}
				}
			}
		}
	}

	for i := 0; i < pkNRows; i++ {
		copy(pk[i*pkRowBytes:], mat[i][pkNRows/8:pkNRows/8+pkRowBytes])
	}

	return true
}

func eval(f *[sysT + 1]gf, a gf) gf {
	r := f[sysT]
	for i := sysT - 1; i >= 0; i-- {
		r = gf4096.Mul(r, a)
		r = gf4096.Add(r, f[i])
	}
	return r
}

func root(out *[sysN]gf, f *[sysT + 1]gf, l *[sysN]gf) {
	for i := 0; i < sysN; i++ {
		out[i] = eval(f, l[i])
	}
}

func shake256(output []byte, input []byte) error {
	shake := sha3.NewShake256()
	_, err := shake.Write(input)
	if err != nil {
		return err
	}
	_, err = shake.Read(output)
	if err != nil {
		return err
	}
	return nil
}

func storeGf(dest []byte, a gf) {
	dest[0] = byte(a & 0xFF)
	dest[1] = byte(a >> 8)
}

func loadGf(src []byte) gf {
	a := uint16(src[1])
	a <<= 8
	a |= uint16(src[0])
	return a & gfMask
}

func load4(in []byte) uint32 {
	ret := uint32(in[3])
	for i := 2; i >= 0; i-- {
		ret <<= 8
		ret |= uint32(in[i])
	}
	return ret
}

func store8(out []byte, in uint64) {
	out[0] = byte((in >> 0x00) & 0xFF)
	out[1] = byte((in >> 0x08) & 0xFF)
	out[2] = byte((in >> 0x10) & 0xFF)
	out[3] = byte((in >> 0x18) & 0xFF)
	out[4] = byte((in >> 0x20) & 0xFF)
	out[5] = byte((in >> 0x28) & 0xFF)
	out[6] = byte((in >> 0x30) & 0xFF)
	out[7] = byte((in >> 0x38) & 0xFF)
}

/*func load8(in []byte) uint64 {
	ret := uint64(in[7])
	for i := 6; i >= 0; i-- {
		ret <<= 8
		ret |= uint64(in[i])
	}
	return ret
}*/

func bitRev(a gf) gf {
	a = ((a & 0x00FF) << 8) | ((a & 0xFF00) >> 8)
	a = ((a & 0x0F0F) << 4) | ((a & 0xF0F0) >> 4)
	a = ((a & 0x3333) << 2) | ((a & 0xCCCC) >> 2)
	a = ((a & 0x5555) << 1) | ((a & 0xAAAA) >> 1)

	return a >> unusedBits
}

type scheme struct{}

var sch kem.Scheme = &scheme{}

// Scheme returns a KEM interface.
func Scheme() kem.Scheme { return sch }

func (*scheme) Name() string               { return "McEliece348864" }
func (*scheme) PublicKeySize() int         { return PublicKeySize }
func (*scheme) PrivateKeySize() int        { return PrivateKeySize }
func (*scheme) SeedSize() int              { return seedSize }
func (*scheme) SharedKeySize() int         { return 0 }
func (*scheme) CiphertextSize() int        { return 0 }
func (*scheme) EncapsulationSeedSize() int { return 0 }

func (sk *PrivateKey) Scheme() kem.Scheme { return sch }
func (pk *PublicKey) Scheme() kem.Scheme  { return sch }

func (sk *PrivateKey) MarshalBinary() ([]byte, error) {
	var ret [PrivateKeySize]byte
	copy(ret[:], sk.sk[:])
	return ret[:], nil
}

func (sk *PrivateKey) Equal(other kem.PrivateKey) bool {
	oth, ok := other.(*PrivateKey)
	if !ok {
		return false
	}
	return bytes.Equal(sk.sk[:], oth.sk[:]) && sk.Public().Equal(other.Public())
}

func (pk *PublicKey) Equal(other kem.PublicKey) bool {
	oth, ok := other.(*PublicKey)
	if !ok {
		return false
	}
	return bytes.Equal(pk.pk[:], oth.pk[:])
}

func (sk *PrivateKey) Public() kem.PublicKey {
	panic("TODO")
}

func (pk *PublicKey) MarshalBinary() ([]byte, error) {
	var ret [PublicKeySize]byte
	copy(ret[:], pk.pk[:])
	return ret[:], nil
}

func (*scheme) GenerateKeyPair() (kem.PublicKey, kem.PrivateKey, error) {
	seed := [32]byte{}
	_, err := cryptoRand.Reader.Read(seed[:])
	if err != nil {
		return nil, nil, err
	}
	pk, sk := deriveKeyPair(seed[:])
	return pk, sk, nil
}

func (*scheme) DeriveKeyPair(seed []byte) (kem.PublicKey, kem.PrivateKey) {
	if len(seed) != seedSize {
		panic("seed must be of length EncapsulationSeedSize")
	}
	return deriveKeyPair(seed)
}

func (*scheme) Encapsulate(pk kem.PublicKey) (ct, ss []byte, err error) {
	panic("TODO")
}

func (*scheme) EncapsulateDeterministically(pk kem.PublicKey, seed []byte) (ct, ss []byte, err error) {
	panic("TODO")
}

func (*scheme) Decapsulate(sk kem.PrivateKey, ct []byte) ([]byte, error) {
	panic("TODO")
}

func (*scheme) UnmarshalBinaryPublicKey(buf []byte) (kem.PublicKey, error) {
	if len(buf) != PublicKeySize {
		return nil, kem.ErrPubKeySize
	}
	pk := [PublicKeySize]byte{}
	copy(pk[:], buf)
	return &PublicKey{pk: pk}, nil
}

func (*scheme) UnmarshalBinaryPrivateKey(buf []byte) (kem.PrivateKey, error) {
	if len(buf) != PrivateKeySize {
		return nil, kem.ErrPrivKeySize
	}
	sk := [PrivateKeySize]byte{}
	copy(sk[:], buf)
	return &PrivateKey{}, nil
}
