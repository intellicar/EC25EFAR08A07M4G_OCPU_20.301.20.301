// Package beacon implements the LA5 beacon wire protocol used by Intellicar's
// telemetry server (as reverse-engineered from the LAFV2 firmware reference
// implementation): frame = "LA5" + BeaconType(u16LE) + SeqID(u16LE) +
// PayloadLen(u16LE) + ModuleCount(1B) + Modules + CRC16(u16LE), where each
// module = Code(u16LE) + Length(u16LE) + Payload.
package beacon

// Beacon types.
const (
	TypeFromDevice = 100 // device -> server, plaintext modules
	TypeSecure     = 102 // encrypted wrapper (see crypto.go)
)

// Module codes used by this implementation. More can be added here as new
// beacon modules are ported - the frame/CRC/transport layers don't change.
// Module codes are only meaningful within their beacon type - e.g. module 6
// means DEVICE_INFO_6 inside a type-100 beacon, but BEACON_ACKS_6 inside a
// type-102 beacon.
const (
	ModuleTimestamp    = 4  // TIMESTAMP_4, type-100 beacon, see modules/timestamp.go
	ModuleDeviceInfo   = 6  // DEVICE_INFO_6, type-100 beacon, see modules/deviceinfo.go
	ModuleGNSSInfo     = 38 // GNSS_INFO_38, type-100 beacon, see modules/gnss.go
	ModuleLoginPubKey  = 1  // login handshake: raw X25519 public key, type-102 beacon
	ModuleChaChaCipher = 2  // ChaCha20-Poly1305 wrapped inner beacon, type-102 beacon
	ModuleBeaconAcks   = 6  // BEACON_ACKS_6: plaintext ack list, type-102 beacon
	ModuleIOData       = 29 // IO_DATA_5, type-100 beacon, see modules/io.go
)

const headerSize = 3 + 2 + 2 + 2 // "LA5" + BeaconType + SeqID + PayloadLen

// maxFrameSize is a sanity cap used only by the inbound parser's resync
// logic (see tryParseFrame) - real frames here are at most a few hundred
// bytes; this just needs to be comfortably larger than any legitimate
// frame while still catching bogus lengths decoded from garbage bytes.
const maxFrameSize = 8192

// Builder assembles one beacon frame: NewBeacon, then zero or more AddModule
// calls, then Finish to patch the length field and append the CRC.
type Builder struct {
	buf []byte
}

// NewBeacon starts a new frame of the given beacon type and sequence ID.
func NewBeacon(beaconType, seqID uint16) *Builder {
	buf := make([]byte, headerSize+1) // +1 for the module-count byte
	copy(buf[0:3], "LA5")
	putU16LE(buf[3:5], beaconType)
	putU16LE(buf[5:7], seqID)
	// buf[7:9] (PayloadLen) is a placeholder, patched in Finish.
	buf[9] = 0 // module count
	return &Builder{buf: buf}
}

// AddModule appends one Code/Length/Payload module and bumps the module count.
func (b *Builder) AddModule(code uint16, payload []byte) {
	header := make([]byte, 4)
	putU16LE(header[0:2], code)
	putU16LE(header[2:4], uint16(len(payload)))
	b.buf = append(b.buf, header...)
	b.buf = append(b.buf, payload...)
	b.buf[headerSize]++ // module-count byte sits right after the header
}

// Finish patches the PayloadLen field and appends the CRC16 trailer,
// returning the complete frame ready to write to the wire.
func (b *Builder) Finish() []byte {
	payloadLen := uint16(len(b.buf) - headerSize) // module-count byte through end of modules
	putU16LE(b.buf[7:9], payloadLen)
	crc := crc16(b.buf)
	crcBytes := make([]byte, 2)
	putU16LE(crcBytes, crc)
	return append(b.buf, crcBytes...)
}

func putU16LE(dst []byte, v uint16) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
}

func getU16LE(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

// ParsedModule is one decoded Code/Payload pair from an inbound frame.
type ParsedModule struct {
	Code    uint16
	Payload []byte
}

// ParsedFrame is a decoded inbound LA5 frame (CRC already verified).
type ParsedFrame struct {
	BeaconType uint16
	SeqID      uint16
	Modules    []ParsedModule
}

// Module returns the payload of the first module matching code, if present.
func (f ParsedFrame) Module(code uint16) ([]byte, bool) {
	for _, m := range f.Modules {
		if m.Code == code {
			return m.Payload, true
		}
	}
	return nil, false
}

// tryParseFrame attempts to decode one frame from the front of buf.
//   - ok, consumed>0: a valid frame was found; caller should drop consumed bytes.
//   - !ok, consumed>0: those leading bytes are garbage (bad magic/CRC/malformed
//     module list); caller should drop them and try again without reading more.
//   - !ok, consumed==0: not enough bytes yet; caller should read more from the stream.
func tryParseFrame(buf []byte) (frame ParsedFrame, consumed int, ok bool) {
	if len(buf) < 3 {
		return ParsedFrame{}, 0, false
	}
	if string(buf[0:3]) != "LA5" {
		return ParsedFrame{}, 1, false // resync: drop one byte and rescan
	}
	if len(buf) < headerSize+1 { // +1 for the module-count byte
		return ParsedFrame{}, 0, false
	}

	payloadLen := int(getU16LE(buf[7:9]))
	total := headerSize + payloadLen + 2 // header + (nModules byte + modules) + CRC
	if total > maxFrameSize {
		// A "LA5" match here is very likely a false positive inside garbage
		// bytes (the real payload-length field would never be this large) -
		// treat it as garbage rather than waiting forever for data this
		// large to arrive.
		return ParsedFrame{}, 1, false
	}
	if len(buf) < total {
		return ParsedFrame{}, 0, false
	}

	if crc16(buf[:total-2]) != getU16LE(buf[total-2:total]) {
		return ParsedFrame{}, 1, false // bad CRC: drop one byte and resync
	}

	nModules := int(buf[9])
	modules := make([]ParsedModule, 0, nModules)
	pos := 10
	end := total - 2 // exclude the trailing CRC
	for i := 0; i < nModules; i++ {
		if pos+4 > end {
			return ParsedFrame{}, 1, false // malformed module list: resync
		}
		code := getU16LE(buf[pos : pos+2])
		mlen := int(getU16LE(buf[pos+2 : pos+4]))
		pos += 4
		if pos+mlen > end {
			return ParsedFrame{}, 1, false
		}
		modules = append(modules, ParsedModule{Code: code, Payload: buf[pos : pos+mlen]})
		pos += mlen
	}

	return ParsedFrame{
		BeaconType: getU16LE(buf[3:5]),
		SeqID:      getU16LE(buf[5:7]),
		Modules:    modules,
	}, total, true
}

// ParseBeaconAcks decodes a BEACON_ACKS_6 payload: count(1B) + SeqID(2B)*count.
func ParseBeaconAcks(payload []byte) []uint16 {
	if len(payload) < 1 {
		return nil
	}
	count := int(payload[0])
	acks := make([]uint16, 0, count)
	pos := 1
	for i := 0; i < count && pos+2 <= len(payload); i++ {
		acks = append(acks, getU16LE(payload[pos:pos+2]))
		pos += 2
	}
	return acks
}
