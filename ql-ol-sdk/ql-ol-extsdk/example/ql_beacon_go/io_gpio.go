package main

/*
#cgo CFLAGS: -march=armv7-a -mfloat-abi=softfp -mfpu=neon
#include <ql_oe.h>
#include <ql_gpio.h>
#include <ql_adc.h>
#include <ql_power_callback.h>
#include <ql_nw.h>

extern void goPowerKeyCallback(int status);

static int register_powerkey_cb(unsigned int shutdown_time) {
    return ql_powerkey_register_cb(shutdown_time, goPowerKeyCallback);
}
*/
import "C"

import (
	"sync"

	"ql_beacon/beacon/modules"
)

// Real hardware wired to each channel. Channels with no real equivalent on
// this hardware are absent from these maps, not stubbed with a fake pin.
var diPins = map[int]C.Enum_PinName{
	modules.DI0: C.PINNAME_GPIO1,
	modules.DI1: C.PINNAME_GPIO2,
	modules.DI2: C.PINNAME_GPIO3,
}

var doPins = map[int]C.Enum_PinName{
	modules.DO0: C.PINNAME_GPIO4,
	modules.DO1: C.PINNAME_GPIO5,
}

var aiChannels = map[int]C.Enum_QADC{
	modules.AI0:  C.ADC0,
	modules.AI1:  C.ADC1,
	modules.AI2:  C.ADC2,
	modules.VBAT: C.CBC,
}

var ioInputsInitialized bool

// powerKeyShutdownThresholdUs matches the module's own documented 650ms
// PWRKEY power-off hold time - anything shorter is a short press, anything
// longer is treated as a long press/shutdown attempt.
const powerKeyShutdownThresholdUs = 700000

var (
	pwrKeyMu       sync.Mutex
	pwrKeyPressed  bool
	pwrKeyLastLong bool
)

//export goPowerKeyCallback
func goPowerKeyCallback(status C.int) {
	pwrKeyMu.Lock()
	pwrKeyPressed = true
	pwrKeyLastLong = status != 0
	pwrKeyMu.Unlock()
}

// takePowerKeyState reports whether PWRKEY was pressed since the last call,
// clearing the latch - PWRKEY is an event (a press), not a continuous level,
// so each beacon reports "pressed since last beacon" rather than a live state.
func takePowerKeyState() bool {
	pwrKeyMu.Lock()
	defer pwrKeyMu.Unlock()
	pressed := pwrKeyPressed
	pwrKeyPressed = false
	return pressed
}

// initIOInputsOnce configures every real digital pin - inputs as inputs,
// outputs as outputs (starting low, since nothing commands them yet) - once.
// ADC/CBC channels need no equivalent setup.
func initIOInputsOnce() {
	if ioInputsInitialized {
		return
	}
	for _, pin := range diPins {
		if ret := C.Ql_GPIO_Init(pin, C.PINDIRECTION_IN, C.PINLEVEL_LOW, C.PINPULLSEL_DISABLE); ret != 0 {
			logf("Ql_GPIO_Init(pin=%d, in) failed: %d", pin, ret)
		}
	}
	for _, pin := range doPins {
		if ret := C.Ql_GPIO_Init(pin, C.PINDIRECTION_OUT, C.PINLEVEL_LOW, C.PINPULLSEL_DISABLE); ret != 0 {
			logf("Ql_GPIO_Init(pin=%d, out) failed: %d", pin, ret)
		}
	}
	if ret := C.register_powerkey_cb(powerKeyShutdownThresholdUs); ret != 0 {
		logf("register_powerkey_cb failed: %d", ret)
	}
	ioInputsInitialized = true
}

// getIOInfo reads every real digital input, digital output, and analog
// channel fresh, right now, and packs the results into IO_DATA_V2_29's
// shape at their spec-defined bit positions.
func getIOInfo() modules.IOInfo {
	initIOInputsOnce()

	var info modules.IOInfo

	for bit, pin := range diPins {
		info.DIEnabled |= 1 << uint(bit)
		lvl := C.Ql_GPIO_GetLevel(pin)
		if lvl < 0 {
			logf("Ql_GPIO_GetLevel(pin=%d) failed: %d", pin, lvl)
			continue
		}
		if lvl != 0 {
			info.DIState |= 1 << uint(bit)
		}
	}

	for bit, pin := range doPins {
		info.DOEnabled |= 1 << uint(bit)
		lvl := C.Ql_GPIO_GetLevel(pin) // reads back what this output is currently driving
		if lvl < 0 {
			logf("Ql_GPIO_GetLevel(pin=%d) failed: %d", pin, lvl)
			continue
		}
		if lvl != 0 {
			info.DOState |= 1 << uint(bit)
		}
	}

	info.DOEnabled |= 1 << uint(modules.DO_COMM_PWR_KEY)
	if takePowerKeyState() {
		info.DOState |= 1 << uint(modules.DO_COMM_PWR_KEY)
	}

	for bit, channel := range aiChannels {
		mv := C.ql_adc_show(channel)
		if mv < 0 {
			logf("ql_adc_show(channel=%d) failed: %d", channel, mv)
			continue
		}
		info.AIEnabled |= 1 << uint(bit)
		info.AIValues[bit] = int32(mv)
	}

	if status := C.QL_NW_GetServiceStatus(); status >= 0 {
		info.AIEnabled |= 1 << uint(modules.COMM)
		info.AIValues[modules.COMM] = int32(status)
	} else {
		logf("QL_NW_GetServiceStatus failed: %d", status)
	}

	return info
}
