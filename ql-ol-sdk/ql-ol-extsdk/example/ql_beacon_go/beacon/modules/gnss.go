package modules

import (
	"encoding/binary"
	"math"
)

// SatConstellationInfo is one constellation's block in GNSS_INFO_38's
// variable-length satellite section - confirmed against
// lafm_beacon_create_100_add_gnss_info_38's sat_info loop. ConstType matches
// the firmware's own encoding: 1=GPS, 2=GLONASS, 3=Galileo, 4=BeiDou.
type SatConstellationInfo struct {
	ConstType   uint8
	NSatUsed    uint8
	NSatVisible uint8
	AvgSNR      uint16 // dB-Hz, unscaled - matches the firmware's own avg_snr
}

// GNSSInfo is GNSS_INFO_38: location/position + satellite data (confirmed
// against lafm_beacon_create_100_add_gnss_info_38 /
// lafm_app_beacon_server_fill_beacon_gnss_info in the firmware).
//
// NSatUsed/PDOP/HDOP/VDOP/UndulationM come from parsing $..GSA/$..GGA NMEA
// text (see the nmea package and gnss_callback.go) - QL_LOC has no
// structured API for any of these. SatInfo's NSatVisible/AvgSNR come from
// QL_LOC's SV_INFO indication instead (confirmed via test_sv_info against
// live $GPGSV ground truth - see gnss_session.go's doc comment).
type GNSSInfo struct {
	LastUpdatedEpoch uint32  // Unix epoch seconds
	LastFixEpoch     uint32  // Unix epoch seconds
	TTFF             uint16  // seconds - approximated as this poll's own latency, not true cold-start TTFF
	FixQuality       uint8   // raw NMEA GGA quality digit (0=no fix, 1=GPS, 2=DGPS, ...)
	LatitudeDeg      float64 // signed degrees; negative = South
	LongitudeDeg     float64 // signed degrees; negative = West
	AltitudeM        float64 // meters
	HeadingDeg       float64 // degrees, 0-360
	SpeedMPS         float64 // meters per second
	NSatUsed         uint8   // total satellites used in the fix (GGA field 7)
	PDOP             float64 // Encode() scales *100, matching the firmware's own wire scale
	HDOP             float64
	VDOP             float64
	UndulationM      float64                // geoid undulation in meters; Encode() scales *100
	SatInfo          []SatConstellationInfo // 0-4 entries, GPS/GLONASS/Galileo/BeiDou order
}

// Encode builds the GNSS_INFO_38 payload, matching the firmware's field
// order and fixed-point scaling exactly:
// last_updated_epoch(u32) + last_fix_epoch(u32) + ttff(u16) + fix_quality(u8)
// + lat_lng_direction(u8) + latitude(u32, |deg|*6,000,000) + longitude(u32,
// same scale) + altitude(i32, m*100) + heading(u16, deg*100) +
// speed_kmph(u16, (m/s->km/h)*100) + nsat_used(u8) + pdop/hdop/vdop(u16,
// dop*100 each) + undulation(i16, m*100) + sat_count(u8) + sat_count *
// { const_type(u8) + nsat_used(u8) + nsat_visible(u8) + avg_snr(u16) }.
func (g GNSSInfo) Encode() []byte {
	buf := make([]byte, 0, 38+5*len(g.SatInfo))
	buf = appendU32(buf, g.LastUpdatedEpoch)
	buf = appendU32(buf, g.LastFixEpoch)
	buf = appendU16(buf, g.TTFF)
	buf = append(buf, g.FixQuality)

	var dir uint8
	if g.LatitudeDeg < 0 {
		dir |= 1 << 0 // South
	}
	if g.LongitudeDeg < 0 {
		dir |= 1 << 1 // West
	}
	buf = append(buf, dir)

	buf = appendU32(buf, scaleAbsDegrees(g.LatitudeDeg))
	buf = appendU32(buf, scaleAbsDegrees(g.LongitudeDeg))
	buf = appendU32(buf, uint32(int32(math.Round(g.AltitudeM*100))))
	buf = appendU16(buf, clampToUint16(math.Round(g.HeadingDeg*100)))
	buf = appendU16(buf, clampToUint16(math.Round(g.SpeedMPS*3.6*100)))

	buf = append(buf, g.NSatUsed)
	buf = appendU16(buf, clampToUint16(math.Round(g.PDOP*100)))
	buf = appendU16(buf, clampToUint16(math.Round(g.HDOP*100)))
	buf = appendU16(buf, clampToUint16(math.Round(g.VDOP*100)))
	buf = appendI16(buf, int16(math.Round(g.UndulationM*100)))

	satCount := len(g.SatInfo)
	if satCount > 4 {
		satCount = 4 // matches the firmware's own 4-constellation cap
	}
	buf = append(buf, uint8(satCount))
	for _, s := range g.SatInfo[:satCount] {
		buf = append(buf, s.ConstType, s.NSatUsed, s.NSatVisible)
		buf = appendU16(buf, s.AvgSNR)
	}

	return buf
}

// encodeCompactGNSSFix builds the 9-field "compact fix" shape - last_updated
// epoch, last_fix epoch, fix_quality, lat_lng_direction, latitude, longitude,
// altitude, heading, speed_kmph - shared verbatim by CELLINFO_WLOC_36's
// embedded GNSS block (see modules/cellinfo.go) and, per the LA5 spec, also
// GNSS_INFO_LOCATION_39/MODULE_GNSS_INFO_LIVE_42 if those are ever added.
// Defined once here since GNSSInfo already carries every field it needs, so
// callers pass in the same value already gathered for GNSS_INFO_38 rather
// than re-fetching GNSS separately.
func encodeCompactGNSSFix(g GNSSInfo) []byte {
	buf := make([]byte, 0, 26)
	buf = appendU32(buf, g.LastUpdatedEpoch)
	buf = appendU32(buf, g.LastFixEpoch)
	buf = append(buf, g.FixQuality)

	var dir uint8
	if g.LatitudeDeg < 0 {
		dir |= 1 << 0 // South
	}
	if g.LongitudeDeg < 0 {
		dir |= 1 << 1 // West
	}
	buf = append(buf, dir)

	buf = appendU32(buf, scaleAbsDegrees(g.LatitudeDeg))
	buf = appendU32(buf, scaleAbsDegrees(g.LongitudeDeg))
	buf = appendU32(buf, uint32(int32(math.Round(g.AltitudeM*100))))
	buf = appendU16(buf, clampToUint16(math.Round(g.HeadingDeg*100)))
	buf = appendU16(buf, clampToUint16(math.Round(g.SpeedMPS*3.6*100)))
	return buf
}

// scaleAbsDegrees converts signed decimal degrees to the firmware's encoding:
// |degrees| * 6,000,000 (equivalent to (deg*60 + min)*100000 + frac_min*100000
// from the original NMEA-derived encoding, simplified since our source is
// already decimal degrees rather than NMEA DDmm.mmmmm).
func scaleAbsDegrees(deg float64) uint32 {
	return uint32(math.Round(math.Abs(deg) * 6_000_000))
}

func clampToUint16(v float64) uint16 {
	if v < 0 {
		return 0
	}
	if v > 65535 {
		return 65535
	}
	return uint16(v)
}

func appendU32(buf []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return append(buf, b...)
}

func appendU16(buf []byte, v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return append(buf, b...)
}

func appendI16(buf []byte, v int16) []byte {
	return appendU16(buf, uint16(v))
}
