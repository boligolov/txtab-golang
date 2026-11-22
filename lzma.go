package txtab

// A small LZMA decoder for ZIP method 14, after the public-domain LzmaSpec reference by Igor Pavlov.
// ZIP's LZMA data is: 2 bytes version, 2 bytes properties size (5), 5 bytes properties, then the raw stream.

import (
	"encoding/binary"
	"errors"
)

var errLZMA = errors.New("corrupt LZMA data")

type rangeDecoder struct {
	in    []byte
	pos   int
	rng   uint32
	code  uint32
	error bool
}

func (rc *rangeDecoder) next() byte {
	if rc.pos >= len(rc.in) {
		rc.error = true
		return 0
	}
	b := rc.in[rc.pos]
	rc.pos++
	return b
}

func (rc *rangeDecoder) init() {
	rc.rng = 0xFFFFFFFF
	if rc.next() != 0 {
		rc.error = true
	}
	for i := 0; i < 4; i++ {
		rc.code = rc.code<<8 | uint32(rc.next())
	}
}

func (rc *rangeDecoder) normalize() {
	if rc.rng < 1<<24 {
		rc.rng <<= 8
		rc.code = rc.code<<8 | uint32(rc.next())
	}
}

func (rc *rangeDecoder) bit(p *uint16) uint32 {
	bound := (rc.rng >> 11) * uint32(*p)
	var b uint32
	if rc.code < bound {
		*p += (2048 - *p) >> 5
		rc.rng = bound
	} else {
		*p -= *p >> 5
		rc.code -= bound
		rc.rng -= bound
		b = 1
	}
	rc.normalize()
	return b
}

func (rc *rangeDecoder) direct(n int) uint32 {
	var res uint32
	for ; n > 0; n-- {
		rc.rng >>= 1
		rc.code -= rc.rng
		t := 0 - (rc.code >> 31)
		rc.code += rc.rng & t
		rc.normalize()
		res = res<<1 + t + 1
	}
	return res
}

func newProbs(n int) []uint16 {
	p := make([]uint16, n)
	for i := range p {
		p[i] = 1024
	}
	return p
}

func (rc *rangeDecoder) tree(p []uint16, bits int) uint32 {
	m := uint32(1)
	for i := 0; i < bits; i++ {
		m = m<<1 + rc.bit(&p[m])
	}
	return m - 1<<bits
}

func (rc *rangeDecoder) reverse(p []uint16, bits int) uint32 {
	m, sym := uint32(1), uint32(0)
	for i := 0; i < bits; i++ {
		b := rc.bit(&p[m])
		m = m<<1 + b
		sym |= b << i
	}
	return sym
}

type lenDecoder struct {
	choice, choice2 uint16
	low, mid        [16][]uint16
	high            []uint16
}

func newLenDecoder() *lenDecoder {
	l := &lenDecoder{choice: 1024, choice2: 1024, high: newProbs(256)}
	for i := range l.low {
		l.low[i], l.mid[i] = newProbs(8), newProbs(8)
	}
	return l
}

func (l *lenDecoder) decode(rc *rangeDecoder, posState uint32) uint32 {
	if rc.bit(&l.choice) == 0 {
		return rc.tree(l.low[posState], 3)
	}
	if rc.bit(&l.choice2) == 0 {
		return 8 + rc.tree(l.mid[posState], 3)
	}
	return 16 + rc.tree(l.high, 8)
}

// decodeZipLZMA decodes ZIP method-14 data to exactly size bytes.
func decodeZipLZMA(data []byte, size uint64) ([]byte, error) {
	if len(data) < 9 || binary.LittleEndian.Uint16(data[2:]) != 5 {
		return nil, errLZMA
	}
	d := int(data[4])
	if d >= 9*5*5 {
		return nil, errLZMA
	}
	lc, lp, pb := d%9, (d/9)%5, d/45
	dictSize := binary.LittleEndian.Uint32(data[5:])
	if dictSize < 1<<12 {
		dictSize = 1 << 12
	}
	rc := &rangeDecoder{in: data[9:]}
	rc.init()

	out := make([]byte, 0, size)
	lit := newProbs(0x300 << (lc + lp))
	isMatch, isRep0Long := newProbs(12<<4), newProbs(12<<4)
	isRep, isRepG0, isRepG1, isRepG2 := newProbs(12), newProbs(12), newProbs(12), newProbs(12)
	var posSlot [4][]uint16
	for i := range posSlot {
		posSlot[i] = newProbs(64)
	}
	posDecoders := newProbs(1 + 128 - 14)
	align := newProbs(16)
	lenDec, repLenDec := newLenDecoder(), newLenDecoder()
	var state, rep0, rep1, rep2, rep3 uint32
	pbMask := uint64(1)<<pb - 1
	lpMask := uint64(1)<<lp - 1

	for uint64(len(out)) < size {
		if rc.error {
			return nil, errLZMA
		}
		total := uint64(len(out))
		posState := uint32(total & pbMask)
		if rc.bit(&isMatch[state<<4+posState]) == 0 {
			prev := uint32(0)
			if len(out) > 0 {
				prev = uint32(out[len(out)-1])
			}
			litState := uint32(total&lpMask)<<lc + prev>>(8-lc)
			probs := lit[0x300*litState:]
			sym := uint32(1)
			if state >= 7 {
				if int(rep0)+1 > len(out) {
					return nil, errLZMA
				}
				matchByte := uint32(out[len(out)-int(rep0)-1])
				for sym < 0x100 {
					matchBit := (matchByte >> 7) & 1
					matchByte <<= 1
					b := rc.bit(&probs[((1+matchBit)<<8)+sym])
					sym = sym<<1 | b
					if matchBit != b {
						break
					}
				}
			}
			for sym < 0x100 {
				sym = sym<<1 | rc.bit(&probs[sym])
			}
			out = append(out, byte(sym-0x100))
			switch {
			case state < 4:
				state = 0
			case state < 10:
				state -= 3
			default:
				state -= 6
			}
			continue
		}
		var length uint32
		if rc.bit(&isRep[state]) != 0 {
			if len(out) == 0 {
				return nil, errLZMA
			}
			if rc.bit(&isRepG0[state]) == 0 {
				if rc.bit(&isRep0Long[state<<4+posState]) == 0 {
					if state < 7 {
						state = 9
					} else {
						state = 11
					}
					out = append(out, out[len(out)-int(rep0)-1])
					continue
				}
			} else {
				var dist uint32
				if rc.bit(&isRepG1[state]) == 0 {
					dist = rep1
				} else {
					if rc.bit(&isRepG2[state]) == 0 {
						dist = rep2
					} else {
						dist = rep3
						rep3 = rep2
					}
					rep2 = rep1
				}
				rep1 = rep0
				rep0 = dist
			}
			length = repLenDec.decode(rc, posState)
			if state < 7 {
				state = 8
			} else {
				state = 11
			}
		} else {
			rep3, rep2, rep1 = rep2, rep1, rep0
			length = lenDec.decode(rc, posState)
			if state < 7 {
				state = 7
			} else {
				state = 10
			}
			lenState := length
			if lenState > 3 {
				lenState = 3
			}
			slot := rc.tree(posSlot[lenState], 6)
			if slot < 4 {
				rep0 = slot
			} else {
				direct := int(slot>>1) - 1
				dist := (2 | slot&1) << direct
				if slot < 14 {
					dist += rc.reverse(posDecoders[dist-slot:], direct)
				} else {
					dist += rc.direct(direct-4) << 4
					dist += rc.reverse(align, 4)
				}
				rep0 = dist
			}
			if rep0 == 0xFFFFFFFF {
				break // end marker
			}
			if rep0 >= dictSize || int(rep0) >= len(out) {
				return nil, errLZMA
			}
		}
		length += 2
		for ; length > 0 && uint64(len(out)) < size; length-- {
			out = append(out, out[len(out)-int(rep0)-1])
		}
	}
	if uint64(len(out)) != size || rc.error {
		return nil, errLZMA
	}
	return out, nil
}
