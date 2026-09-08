package modules

import "encoding/binary"

// AccelInfo is ACC_MOV_INFO_31: a windowed movement-activity summary, not raw
// XYZ samples. LastWinSize/ResSumLast describe the most recently completed
// window; CurrWinSize/ResSumCurr describe the in-progress one. The two
// counters accumulate since the last beacon and are reset each time Encode
// is read via getAccelInfo (see movinfo_i2c.go).
type AccelInfo struct {
	Whoami         uint8  // raw LIS2DS12 WHO_AM_I byte (0x43) - same numeric value as the spec's own chip-ID enum
	LastWinSize    uint16
	ResSumLast     uint32
	CurrWinSize    uint16
	ResSumCurr     uint32
	ResCntAboveThr uint32
	ResCntTotal    uint32
}

// Encode builds the ACC_MOV_INFO_31 payload: whoami(1B) + last_win_size(2B) +
// res_sum_last(4B) + curr_win_size(2B) + res_sum_curr(4B) +
// res_cnt_above_thr(4B) + res_cnt_total(4B), all little-endian - 21 bytes total.
func (a AccelInfo) Encode() []byte {
	buf := make([]byte, 21)
	buf[0] = a.Whoami
	binary.LittleEndian.PutUint16(buf[1:3], a.LastWinSize)
	binary.LittleEndian.PutUint32(buf[3:7], a.ResSumLast)
	binary.LittleEndian.PutUint16(buf[7:9], a.CurrWinSize)
	binary.LittleEndian.PutUint32(buf[9:13], a.ResSumCurr)
	binary.LittleEndian.PutUint32(buf[13:17], a.ResCntAboveThr)
	binary.LittleEndian.PutUint32(buf[17:21], a.ResCntTotal)
	return buf
}
