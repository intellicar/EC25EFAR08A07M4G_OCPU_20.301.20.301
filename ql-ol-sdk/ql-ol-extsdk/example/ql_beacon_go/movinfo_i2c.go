package main

/*
#include "ql_i2c.h"
*/
import "C"

import (
	"math"
	"sync"
	"time"
	"unsafe"

	"ql_beacon/beacon/modules"
)

// chip wired to bus
const (
	accelI2CDev    = "/dev/i2c-2"
	accelSlaveAddr = 0x1D
	accelWhoAmI    = 0x0F
	accelWhoAmIVal = 0x43
	accelCtrl1     = 0x20
	accelCtrl1Cfg  = 0x40        // 100Hz ODR, +/-2g, high-resolution mode
	accelOutXL     = 0x28 | 0x80 // auto-increment bit set, 6-byte XYZ burst

	accelSensitivity = 0.061 // mg/LSB at +/-2g (datasheet Table, matches CTRL1 config above)
	accelWinSize     = 6     // samples per window
	accelThreshold   = 10.0  // mg difference between COMPLETED windows counted as "movement"
	accelPollEvery   = 40 * time.Millisecond
)

var (
	accelMu        sync.Mutex
	accelWhoami    uint8
	accelCurrSum   uint32
	accelCurrCount uint16
	accelLastSum   uint32
	accelLastCount uint16
	accelAboveThr  uint32
	accelTotalWin  uint32
)

// startAccelSampling opens the accelerometer once and polls it forever in a
// background goroutine, maintaining a sliding two-window magnitude sum -
func startAccelSampling() {
	fd := C.Ql_I2C_Init(C.CString(accelI2CDev))
	if fd < 0 {
		logf("accel: failed to open %s", accelI2CDev)
		return
	}

	var who [1]byte
	if r := C.Ql_I2C_Read(fd, C.ushort(accelSlaveAddr), C.uchar(accelWhoAmI), (*C.uchar)(unsafe.Pointer(&who[0])), 1); r != 0 || who[0] != accelWhoAmIVal {
		logf("accel: WHO_AM_I check failed (got 0x%02X)", who[0])
		C.Ql_I2C_Deinit(fd)
		return
	}
	accelMu.Lock()
	accelWhoami = who[0]
	accelMu.Unlock()

	cfg := [1]byte{accelCtrl1Cfg}
	if r := C.Ql_I2C_Write(fd, C.ushort(accelSlaveAddr), C.uchar(accelCtrl1), (*C.uchar)(unsafe.Pointer(&cfg[0])), 1); r != 0 {
		logf("accel: CTRL1 config write failed")
		C.Ql_I2C_Deinit(fd)
		return
	}
	time.Sleep(10 * time.Millisecond)

	go func() {
		defer C.Ql_I2C_Deinit(fd)
		for {
			time.Sleep(accelPollEvery)

			var raw [6]byte
			if r := C.Ql_I2C_Read(fd, C.ushort(accelSlaveAddr), C.uchar(accelOutXL), (*C.uchar)(unsafe.Pointer(&raw[0])), 6); r != 0 {
				logf("accel: burst read failed")
				continue
			}

			x := int16(uint16(raw[0]) | uint16(raw[1])<<8)
			y := int16(uint16(raw[2]) | uint16(raw[3])<<8)
			z := int16(uint16(raw[4]) | uint16(raw[5])<<8)
			mag := math.Sqrt(float64(x)*float64(x)+float64(y)*float64(y)+float64(z)*float64(z)) * accelSensitivity

			accelMu.Lock()
			accelCurrSum += uint32(mag)
			accelCurrCount++
			if accelCurrCount >= accelWinSize {
				newAvg := float64(accelCurrSum) / float64(accelCurrCount)
				// Only compare once a previous window actually exists - the
				// very first window ever has nothing to compare against.
				if accelLastCount > 0 {
					prevAvg := float64(accelLastSum) / float64(accelLastCount)
					if math.Abs(newAvg-prevAvg) > accelThreshold {
						accelAboveThr++
					}
				}
				accelTotalWin++
				accelLastSum, accelLastCount = accelCurrSum, accelCurrCount
				accelCurrSum, accelCurrCount = 0, 0
			}
			accelMu.Unlock()
		}
	}()
}

// getAccelInfo snapshots the current windowing state for one beacon and
// resets the since-last-beacon counters, matching the spec's own semantics
func getAccelInfo() modules.AccelInfo {
	accelMu.Lock()
	defer accelMu.Unlock()

	info := modules.AccelInfo{
		Whoami:         accelWhoami,
		LastWinSize:    accelLastCount,
		ResSumLast:     accelLastSum,
		CurrWinSize:    accelCurrCount,
		ResSumCurr:     accelCurrSum,
		ResCntAboveThr: accelAboveThr,
		ResCntTotal:    accelTotalWin,
	}
	accelAboveThr, accelTotalWin = 0, 0
	return info
}
