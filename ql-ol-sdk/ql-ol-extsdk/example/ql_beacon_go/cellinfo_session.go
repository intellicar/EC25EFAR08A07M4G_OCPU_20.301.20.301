package main

/*
#include <ql_oe.h>
#include "ql_mcm_nw.h"
#include "ql_mcm_atc.h"
#include "ql_nw.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"ql_beacon/beacon/modules"
)

// getCellInfo gathers CELLINFO_WLOC_36/CELLINFO_SCELL_37's underlying data in
// one QL_MCM_NW client lifecycle (matching the same short-lived
// init/query/deinit pattern already used by getIMEI/getModemFirmware/
// getICCID/getNetOperator in main.go), so main.go's sendOnce can build both
// modules from a single call instead of querying the modem twice.
//
// Serving-cell identity (mcc/mnc/cid/lac/tac/radio_tech) comes from
// QL_MCM_NW_GetRegStatus's data_registration_details_3gpp - "what am I
// registered to" is unambiguous there, unlike QL_MCM_NW_GetCellInfo's arrays
// which carry no per-entry serving/neighbor flag. ARFCN/EARFCN/PCI/BSIC and
// the neighbor list itself come from QL_MCM_NW_GetCellInfo, matched against
// the registered cid to fill in the serving entry's radio-specific fields and
// separate out neighbors. Signal quality (RSSI/RSRP/SINR, and an approximate
// CSQ/RxLev) comes from QL_MCM_NW_GetSignalStrength.
//
// Neighbor cells reuse the serving cell's MCC/MNC rather than decoding each
// entry's own BCD-packed plmn field (3GPP TS 24.008 Section 10.5.1.3) - a
// deliberate simplification, since neighboring towers are the same operator
// in the overwhelming majority of cases and getting this wrong only affects
// a rarely-used display field, not cell identification (cid/tac/earfcn are
// still exact).
func getCellInfo() (serving modules.CellRecord, neighbors []modules.CellRecord, lastUpdatedEpoch uint32, err error) {
	var hNw C.nw_client_handle_type
	if ret := C.QL_MCM_NW_Client_Init(&hNw); ret != C.E_QL_SUCCESS {
		return modules.CellRecord{}, nil, 0, fmt.Errorf("QL_MCM_NW_Client_Init failed: %d", ret)
	}
	defer C.QL_MCM_NW_Client_Deinit(hNw)

	var reg C.QL_MCM_NW_REG_STATUS_INFO_T
	if ret := C.QL_MCM_NW_GetRegStatus(hNw, &reg); ret != C.E_QL_SUCCESS {
		return modules.CellRecord{}, nil, 0, fmt.Errorf("QL_MCM_NW_GetRegStatus failed: %d", ret)
	}
	if reg.data_registration_details_3gpp_valid == 0 {
		return modules.CellRecord{}, nil, 0, fmt.Errorf("no 3GPP data registration details available")
	}
	d3 := reg.data_registration_details_3gpp
	isLTE := d3.radio_tech == C.E_QL_MCM_NW_RADIO_TECH_LTE
	mcc, _ := strconv.Atoi(C.GoString(&d3.mcc[0]))
	mnc, _ := strconv.Atoi(C.GoString(&d3.mnc[0]))

	serving = modules.CellRecord{
		IsLTE:  isLTE,
		MCC:    uint16(mcc),
		MNC:    uint16(mnc),
		CellID: uint32(d3.cid),
	}
	if isLTE {
		serving.LacTac = uint16(d3.tac)
	} else {
		serving.LacTac = uint16(d3.lac)
	}

	// Fill in ARFCN/EARFCN/PCI/BSIC for the serving cell and split out
	// neighbors, matched by cid against the registered serving cell above.
	var cellInfo C.QL_MCM_NW_CELL_INFO_T
	if ret := C.QL_MCM_NW_GetCellInfo(hNw, &cellInfo); ret == C.E_QL_SUCCESS {
		if isLTE && cellInfo.lte_info_valid != 0 {
			n := int(cellInfo.lte_info_len)
			for i := 0; i < n; i++ {
				e := cellInfo.lte_info[i]
				if uint32(e.cid) == serving.CellID {
					serving.PCI = uint16(e.pci)
					serving.ArfcnEarfcn = uint16(e.earfcn)
					continue
				}
				neighbors = append(neighbors, modules.CellRecord{
					IsLTE:       true,
					MCC:         serving.MCC,
					MNC:         serving.MNC,
					LacTac:      uint16(e.tac),
					CellID:      uint32(e.cid),
					PCI:         uint16(e.pci),
					ArfcnEarfcn: uint16(e.earfcn),
				})
			}
		} else if !isLTE && cellInfo.gsm_info_valid != 0 {
			n := int(cellInfo.gsm_info_len)
			for i := 0; i < n; i++ {
				e := cellInfo.gsm_info[i]
				if uint32(e.cid) == serving.CellID {
					serving.ArfcnEarfcn = uint16(e.arfcn)
					serving.Bsic = uint16(e.bsic)
					continue
				}
				neighbors = append(neighbors, modules.CellRecord{
					IsLTE:       false,
					MCC:         serving.MCC,
					MNC:         serving.MNC,
					LacTac:      uint16(e.lac),
					CellID:      uint32(e.cid),
					ArfcnEarfcn: uint16(e.arfcn),
					Bsic:        uint16(e.bsic),
				})
			}
		}
	}

	var sig C.QL_MCM_NW_SIGNAL_STRENGTH_INFO_T
	if ret := C.QL_MCM_NW_GetSignalStrength(hNw, &sig); ret == C.E_QL_SUCCESS {
		if isLTE && sig.lte_sig_info_valid != 0 {
			serving.RxDBm = int8(sig.lte_sig_info.rssi)
			serving.RSSI = int8(sig.lte_sig_info.rssi)
			serving.RSRP = int8(sig.lte_sig_info.rsrp)     // native range (-44 to -140) fits int8 directly
			serving.SINR = int8(sig.lte_sig_info.snr / 10) // native snr is in 0.1dB units; wire SINR is whole dB
			// LTE has no standard RXLEV concept, but the wire format carries
			// the field for both RATs - approximate it the same way as GSM.
			serving.RxLev = rxLevFromRSSI(int(sig.lte_sig_info.rssi))
		} else if !isLTE && sig.gsm_sig_info_valid != 0 {
			serving.RxDBm = int8(sig.gsm_sig_info.rssi)
			serving.RxLev = rxLevFromRSSI(int(sig.gsm_sig_info.rssi))
		}
	}

	enrichNeighborSignal(neighbors)

	return serving, neighbors, uint32(time.Now().Unix()), nil
}

// enrichNeighborSignal fills in RSRP/RSSI/SINR/RxLev/RxDBm for the neighbor
// cells gathered above via QL_MCM_NW_GetCellInfo, which has no signal fields
// for neighbor entries at all - confirmed as a genuine gap in every
// structured native API on this platform (also checked QL_MCM_NW_PerformScan
// and ql_nw.h's QL_NW_GetServingCell - neither has per-neighbor signal data
// either). AT+QENG is the only source for this on this chip, same as why
// LAFV2's own EC25E code queries AT+QENG="neighbourcell" directly.
//
// Field order confirmed empirically against this exact device across 6 real
// captures (not just the EC200U/EG915U doc, which lists RSRP before RSRQ -
// backwards from what this firmware actually sends): "neighbourcell
// intra","LTE",<EARFCN>,<PCI>,<RSRQ>,<RSRP>,<RSSI>,<SINR>,<srxlev>,... -
// cross-checked against the serving cell's own known RSRP/RSRQ in the same
// captures.
//
// "neighbourcell inter" (inter-frequency) rows are skipped - every capture
// observed had all-zero RSRQ/RSRP/RSSI there, i.e. genuinely not measured.
//
// Matched against neighbors by (EARFCN, PCI) - the only two fields both
// QL_MCM_NW_GetCellInfo and AT+QENG reliably agree on. A neighbor with no
// matching AT+QENG line keeps its existing (zero) signal fields; an AT+QENG
// line with no matching neighbor (the two queries aren't perfectly
// simultaneous, so their neighbor sets can differ slightly) is dropped
// rather than added as a new entry, since it would have no CID/TAC to go
// with the signal data.
func enrichNeighborSignal(neighbors []modules.CellRecord) {
	if len(neighbors) == 0 {
		return
	}

	var hAtc C.atc_client_handle_type
	if ret := C.QL_ATC_Client_Init(&hAtc); ret != 0 {
		logf("QL_ATC_Client_Init failed: %d", ret)
		return
	}
	defer C.QL_ATC_Client_Deinit(hAtc)

	cmd := C.CString("AT+QENG=\"neighbourcell\"\r\n")
	defer C.free(unsafe.Pointer(cmd))

	const respBufLen = 4096
	respBuf := make([]byte, respBufLen)
	if ret := C.QL_ATC_Send_Cmd(hAtc, cmd, (*C.char)(unsafe.Pointer(&respBuf[0])), C.int(respBufLen)); ret != 0 {
		logf(`QL_ATC_Send_Cmd(AT+QENG="neighbourcell") failed: %d`, ret)
		return
	}
	resp := C.GoString((*C.char)(unsafe.Pointer(&respBuf[0])))

	const prefix = `+QENG: "neighbourcell intra","LTE",`
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		f := strings.Split(strings.TrimPrefix(line, prefix), ",")
		if len(f) < 6 {
			continue
		}
		earfcn, e1 := strconv.Atoi(f[0])
		pci, e2 := strconv.Atoi(f[1])
		_, e3 := strconv.Atoi(f[2]) // rsrq - no wire slot for it (CELLINFO_WLOC_36 has no RSRQ field), parsed only to validate the line
		rsrp, e4 := strconv.Atoi(f[3])
		rssi, e5 := strconv.Atoi(f[4])
		sinrRaw, e6 := strconv.Atoi(f[5])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
			continue
		}

		for i := range neighbors {
			n := &neighbors[i]
			if !n.IsLTE || int(n.ArfcnEarfcn) != earfcn || int(n.PCI) != pci {
				continue
			}
			n.RxDBm = int8(rssi)
			n.RSSI = int8(rssi)
			n.RSRP = int8(rsrp)
			n.RxLev = rxLevFromRSSI(rssi)
			// Raw SINR's documented valid range is 7-107; the -20 seen
			// whenever a neighbor's SINR genuinely wasn't measured falls
			// outside that, so it's left at the zero default rather than
			// run through the conversion formula.
			if sinrRaw >= 7 && sinrRaw <= 107 {
				n.SINR = int8(math.Round(float64(sinrRaw)/2 - 23.5))
			}
			break
		}
	}
}

// getNativeCSQ fetches CSQ directly via ql_nw.h's QL_NW_GetCSQ, which uses
// the modem's own (113+rssi)/2 computation - more accurate than deriving it
// via csqFromRSSI from QL_MCM_NW_GetSignalStrength's RSSI, so this is
// preferred; csqFromRSSI remains as the fallback if this call fails.
func getNativeCSQ() (uint8, error) {
	var csq C.int
	if ret := C.QL_NW_GetCSQ(&csq); ret != 0 {
		return 0, fmt.Errorf("QL_NW_GetCSQ failed: %d", ret)
	}
	return uint8(csq), nil
}

// rxLevFromRSSI approximates GSM 05.08 RXLEV (0-63) from RSSI dBm, since
// QL_MCM_NW_GSM_SIGNAL_INFO_T only exposes rssi, not a pre-computed rxlev.
func rxLevFromRSSI(dbm int) uint8 {
	lev := dbm + 110
	if lev < 0 {
		return 0
	}
	if lev > 63 {
		return 63
	}
	return uint8(lev)
}

// csqFromRSSI approximates the AT+CSQ-style 0-31 scale (3GPP TS 27.007) from
// RSSI dBm, for CELLINFO_SCELL_37's CSQ field. Only used as a fallback when
// getNativeCSQ (the real modem-computed value) fails.
func csqFromRSSI(dbm int) uint8 {
	csq := (dbm + 113) / 2
	if csq < 0 {
		return 0
	}
	if csq > 31 {
		return 31
	}
	return uint8(csq)
}
