package modules

import "encoding/binary"

// Digital Input bit indices
const (
	DI0 = 0
	DI1 = 1
	// VIN = 2 - no ignition-sense circuit on this hardware
	DI2 = 3
)

// Digital Output bit indices
const (
	// DO_SMPS = 0 - SMPS is external to the module, nothing to drive here
	DO0 = 1
	DO1 = 2
	// DO_COPROC_5V = 3, DO_COMM_PWR = 4 - no coprocessor, no readable path to VBAT_BB
	DO_COMM_PWR_KEY = 5
	// DO_EN_COPROC_PWR_CTRL = 8, DO_EN_COPROC_DOUT_CTRL = 9 - no coprocessor
	// DO_EN_UNPLUG_DETECT = 10 - requires backup-battery hardware not present
	// DO_INTBAT_POWER_CTRL = 11, DO_INTBAT_CHARGE_CTRL = 12 - no internal battery circuit
)

// Analog Input bit indices
const (
	AI0 = 0
	AI1 = 1
	// VIN = 2, VIN_CURR = 3 - no ignition sense circuit, no current sensing on this hardware
	VBAT = 4
	// VBAT_CURR = 5 - no current-sensing hardware, internal or external, on this module
	// CANL = 6, CANH = 7 - no CAN controller on this hardware
	AI2  = 8
	COMM = 9
	// SW_5V = 10 - no switchable 5V rail on this hardware
)

// Each enable/state field is a 3-byte (24-bit) bitmask.
type IOInfo struct {
	DIEnabled uint32
	DIState   uint32
	DOEnabled uint32
	DOState   uint32
	AIEnabled uint32
	AIValues  [24]int32
}

// Encode builds the IO_DATA_V2_29 payload: din_en(3B) + din_state(3B) +
// dout_en(3B) + dout_state(3B) + ain_en(3B) + { ain_values[i](4B LE) for
// each i where ain_en bit i is set, ascending bit order } + counters(1B).
func (io IOInfo) Encode() []byte {
	buf := make([]byte, 0, 15+4*24+1)
	buf = append24(buf, io.DIEnabled)
	buf = append24(buf, io.DIState)
	buf = append24(buf, io.DOEnabled)
	buf = append24(buf, io.DOState)
	buf = append24(buf, io.AIEnabled)

	for i := 0; i < 24; i++ {
		if io.AIEnabled&(1<<uint(i)) != 0 {
			b := make([]byte, 4)
			binary.LittleEndian.PutUint32(b, uint32(io.AIValues[i]))
			buf = append(buf, b...)
		}
	}

	return append(buf, 0) // counters placeholder
}

func append24(buf []byte, v uint32) []byte {
	return append(buf, byte(v), byte(v>>8), byte(v>>16))
}
