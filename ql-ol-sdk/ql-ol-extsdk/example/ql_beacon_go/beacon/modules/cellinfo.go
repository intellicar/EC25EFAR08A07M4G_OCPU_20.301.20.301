package modules

// CellRecord is one GSM or LTE cell entry (serving or neighbor) inside
// CELLINFO_WLOC_36, matching the LA5 spec's "Scell fields"/"Ncell blocks"
// note exactly: LTE = MCC,MNC,TAC,CELLID,PCI,EARFCN,rxlev,rxdbm,RSRP,RSSI,SINR;
// GSM = MCC,MNC,LAC,CELLID,BSIC,ARFCN,rxlev,rxdbm. This is a different (and
// wider) shape than CellInfoScell below - CELLINFO_SCELL_37 has no
// PCI/RSRP/RSSI/SINR/rxlev/bsic, but does have CSQ, which this record doesn't.
type CellRecord struct {
	IsLTE bool
	MCC   uint16
	MNC   uint16
	// LacTac is LAC for GSM, TAC for LTE.
	LacTac uint16
	// CellID is stored as the full value; encode() writes only 2 bytes on
	// the wire for GSM (matching the firmware's own field width) and all 4
	// bytes for LTE.
	CellID uint32
	// PCI is LTE-only; ignored when !IsLTE.
	PCI uint16
	// ArfcnEarfcn is ARFCN for GSM, EARFCN for LTE.
	ArfcnEarfcn uint16
	// Bsic is GSM-only, 2 bytes on the wire (matches
	// lafm_beacon_create_100_add_cellinfo_wloc_36_cellinfo_gsm's
	// memcpy(..., &cellinfo_gsm->bsic, 2), not 1 byte); ignored when IsLTE.
	Bsic uint16
	// RxLev is common to both GSM and LTE records on the wire (LTE has no
	// standard RXLEV concept, but the firmware's own struct carries the
	// field for both RATs regardless - see rxLevFromRSSI in
	// cellinfo_session.go for how it's approximated for both).
	RxLev uint8
	// RxDBm is common to both GSM and LTE records.
	RxDBm int8
	// RSRP/RSSI/SINR are LTE-only; ignored when !IsLTE.
	RSRP int8
	RSSI int8
	SINR int8
}

// encode matches lafm_beacon_create_100_add_cellinfo_wloc_36_cellinfo_{lte,gsm}
// field order exactly:
// LTE:  mcc,mnc,tac,cellid(4B),pci,earfcn,rx_lev,rxdbm,rsrp,rssi,sinr = 19B
// GSM:  mcc,mnc,lac,cellid(2B),bsic(2B),arfcn,rx_lev,rxdbm            = 14B
func (c CellRecord) encode() []byte {
	if c.IsLTE {
		buf := make([]byte, 0, 19)
		buf = appendU16(buf, c.MCC)
		buf = appendU16(buf, c.MNC)
		buf = appendU16(buf, c.LacTac)
		buf = appendU32(buf, c.CellID)
		buf = appendU16(buf, c.PCI)
		buf = appendU16(buf, c.ArfcnEarfcn)
		buf = append(buf, c.RxLev, byte(c.RxDBm), byte(c.RSRP), byte(c.RSSI), byte(c.SINR))
		return buf
	}
	buf := make([]byte, 0, 14)
	buf = appendU16(buf, c.MCC)
	buf = appendU16(buf, c.MNC)
	buf = appendU16(buf, c.LacTac)
	buf = appendU16(buf, uint16(c.CellID))
	buf = appendU16(buf, c.Bsic)
	buf = appendU16(buf, c.ArfcnEarfcn)
	buf = append(buf, c.RxLev, byte(c.RxDBm))
	return buf
}

// CellInfoWLoc is CELLINFO_WLOC_36: a compact GNSS fix bundled with serving +
// neighbor cell info. LAFV2's own EC25E reference firmware never sends this
// module - it's only wired up for EC200U/EC200U_EU/EG800G/MC60 boards (see
// lafm_app_beacon_server.c's board #if chain); EC25E only sends
// CELLINFO_SCELL_37 (serving-cell only, below). But QL_MCM_NW_GetCellInfo
// exposes neighbor cells on EC25E too via the native QuecOpen API (unlike the
// AT+QENG path LAFV2 itself uses on this board), so this module is populated
// and sent here in addition to 37.
type CellInfoWLoc struct {
	// GNSS is the same value already gathered for GNSS_INFO_38 this beacon
	// cycle - passed straight through rather than re-fetched, since
	// encodeCompactGNSSFix only needs a subset of it.
	GNSS GNSSInfo

	CellLastUpdatedEpoch uint32
	Serving              CellRecord
	Neighbors            []CellRecord
}

// Encode builds the CELLINFO_WLOC_36 payload: the 26-byte compact GNSS block
// (see encodeCompactGNSSFix in gnss.go) followed by
// cellinfo_last_updated(u32) + serving_cell_type(u8) + scell record +
// ncell_count(u8) + ncell_count * (cell_type(u8) + record).
func (c CellInfoWLoc) Encode() []byte {
	buf := make([]byte, 0, 26+6+19*(1+len(c.Neighbors)))
	buf = append(buf, encodeCompactGNSSFix(c.GNSS)...)

	buf = appendU32(buf, c.CellLastUpdatedEpoch)
	buf = append(buf, boolToU8(c.Serving.IsLTE))
	buf = append(buf, c.Serving.encode()...)

	neighbors := c.Neighbors
	if len(neighbors) > 255 {
		neighbors = neighbors[:255]
	}
	buf = append(buf, uint8(len(neighbors)))
	for _, n := range neighbors {
		buf = append(buf, boolToU8(n.IsLTE))
		buf = append(buf, n.encode()...)
	}

	return buf
}

// CellInfoScell is CELLINFO_SCELL_37: serving-cell-only info, matching the LA5
// spec's CELLINFO_SCELL_37 table exactly (Last_Updated_s, isLTE, MCC, MNC,
// TAC_LAC, CELLID, E_ARFCN, CSQ, RXDBM). This is the module LAFV2's own EC25E
// reference firmware actually sends every beacon cycle (see
// lafm_app_beacon_server_send_statusinfo's non-MC60 #else branch) -
// CellInfoWLoc above is additional data this Go implementation sends beyond
// what stock EC25E firmware does.
type CellInfoScell struct {
	LastUpdatedEpoch uint32
	IsLTE            bool
	MCC              uint16
	MNC              uint16
	TACLAC           uint16
	CellID           uint32
	EArfcn           uint16
	CSQ              uint8
	RxDBm            int8
}

func (c CellInfoScell) Encode() []byte {
	buf := make([]byte, 0, 19)
	buf = appendU32(buf, c.LastUpdatedEpoch)
	buf = append(buf, boolToU8(c.IsLTE))
	buf = appendU16(buf, c.MCC)
	buf = appendU16(buf, c.MNC)
	buf = appendU16(buf, c.TACLAC)
	buf = appendU32(buf, c.CellID)
	buf = appendU16(buf, c.EArfcn)
	buf = append(buf, c.CSQ, byte(c.RxDBm))
	return buf
}

func boolToU8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
