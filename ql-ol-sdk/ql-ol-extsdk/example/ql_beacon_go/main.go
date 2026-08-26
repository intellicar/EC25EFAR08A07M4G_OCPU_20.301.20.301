// ql_beacon periodically sends a basic device-info beacon to Intellicar's
// telemetry server, using the LA5 beacon protocol (see the beacon package).
package main

/*
#cgo CFLAGS: -march=armv7-a -mfloat-abi=softfp -mfpu=neon
#include <ql_oe.h>
#include "ql_mcm_sim.h"
#include "ql_mcm_nw.h"
#include "ql_mcm_dm.h"
*/
import "C"

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ql_beacon/beacon"
	"ql_beacon/beacon/modules"
)

const (
	serverAddr   = "la5.intellicar.in:10127"
	identityPath = "/data/persist/ql_beacon_devicekey"
	sendInterval = 30 * time.Second

	// queueDir is the durable beacon queue - see beacon.PendingStore - kept
	// on /mnt, the UBIFS filesystem mounted on the Winbond W25N01GW
	// SPI-NAND (see ql-ol-rootfs/etc/init.d/find_partitions.sh), so a
	// beacon written here survives a reboot, a watchdog reset, or a
	// network drop that forces a reconnect.
	queueDir = "/mnt/ql_beacon/queue"

	// Matches ql_autoconnect's fixed deployment value (same device, same SIM).
	apn = "airteliot.com"

	// Shipped by the SDK on every build - see quectel-project-version for
	// the non-sub variant if a coarser string is ever needed instead.
	versionFilePath = "/etc/quectel-project-subversion"
)

// getFWVersion reads the build revision the SDK already stamps onto every
// image, instead of hardcoding it as a string constant here.
func getFWVersion() (string, error) {
	data, err := os.ReadFile(versionFilePath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", versionFilePath, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, ":")
		if found && strings.TrimSpace(key) == "Project Sub Rev" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", fmt.Errorf("%s: \"Project Sub Rev\" line not found", versionFilePath)
}

// deviceIDFromIMEI derives DEVICEID from the IMEI, left-padded with zeros to
// 16 characters to match the firmware's DEVICEID length convention.
func deviceIDFromIMEI(imei string) string {
	if len(imei) >= 16 {
		return imei
	}
	return strings.Repeat("0", 16-len(imei)) + imei
}

func logf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stdout, "[ql_beacon] "+format+"\n", args...)
}

// getUptimeMs returns real device uptime (matching LAFV2's
// lafm_timer_get_uptime_ms, used in TIMESTAMP_4 - see modules/timestamp.go),
// not this process's own runtime.
func getUptimeMs() uint64 {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0
	}
	return uint64(info.Uptime) * 1000
}

func getIMEI() (string, error) {
	var hDm C.dm_client_handle_type
	if ret := C.QL_MCM_DM_Client_Init(&hDm); ret != C.E_QL_SUCCESS {
		return "", fmt.Errorf("QL_MCM_DM_Client_Init failed: %d", ret)
	}
	defer C.QL_MCM_DM_Client_Deinit(hDm)

	var sn C.ql_dm_device_serial_numbers_t
	if ret := C.QL_MCM_DM_GetSerialNumbers(hDm, &sn); ret != C.E_QL_SUCCESS {
		return "", fmt.Errorf("QL_MCM_DM_GetSerialNumbers failed: %d", ret)
	}
	if sn.imei_valid == 0 {
		return "", fmt.Errorf("IMEI not valid")
	}
	return C.GoString(&sn.imei[0]), nil
}

func getModemFirmware() (string, error) {
	var hDm C.dm_client_handle_type
	if ret := C.QL_MCM_DM_Client_Init(&hDm); ret != C.E_QL_SUCCESS {
		return "", fmt.Errorf("QL_MCM_DM_Client_Init failed: %d", ret)
	}
	defer C.QL_MCM_DM_Client_Deinit(hDm)

	var rev C.ql_dm_device_rev_id_t
	if ret := C.QL_MCM_DM_GetFirmware(hDm, &rev); ret != C.E_QL_SUCCESS {
		return "", fmt.Errorf("QL_MCM_DM_GetFirmware failed: %d", ret)
	}
	return C.GoString(&rev.device_rev_id[0]), nil
}

func getICCID() (string, error) {
	var hSim C.sim_client_handle_type
	if ret := C.QL_MCM_SIM_Client_Init(&hSim); ret != C.int(C.E_QL_SUCCESS) {
		return "", fmt.Errorf("QL_MCM_SIM_Client_Init failed: %d", ret)
	}
	defer C.QL_MCM_SIM_Client_Deinit(hSim)

	buf := make([]C.char, 32)
	if ret := C.QL_MCM_SIM_GetICCID(hSim, C.E_QL_MCM_SIM_SLOT_ID_1, &buf[0], C.size_t(len(buf))); ret != C.E_QL_SUCCESS {
		return "", fmt.Errorf("QL_MCM_SIM_GetICCID failed: %d", ret)
	}
	return C.GoString(&buf[0]), nil
}

func getNetOperator() (string, error) {
	var hNw C.nw_client_handle_type
	if ret := C.QL_MCM_NW_Client_Init(&hNw); ret != C.E_QL_SUCCESS {
		return "", fmt.Errorf("QL_MCM_NW_Client_Init failed: %d", ret)
	}
	defer C.QL_MCM_NW_Client_Deinit(hNw)

	var info C.QL_MCM_NW_OPERATOR_NAME_INFO_T
	if ret := C.QL_MCM_NW_GetOperatorName(hNw, &info); ret != C.E_QL_SUCCESS {
		return "", fmt.Errorf("QL_MCM_NW_GetOperatorName failed: %d", ret)
	}
	return C.GoString(&info.long_eons[0]), nil
}

// gatherDeviceInfo queries whatever it can and logs (but doesn't fail on)
// individual lookups that come back empty - a partial beacon is still useful.
// pubKey is passed in raw (not hex-encoded - see DeviceInfo.PubKey) rather
// than re-derived, since it's fixed for the life of the process (see
// beacon.Identity).
func gatherDeviceInfo(pubKey []byte) modules.DeviceInfo {
	imei, err := getIMEI()
	if err != nil {
		logf("getIMEI: %v", err)
	}
	fwVersion, err := getFWVersion()
	if err != nil {
		logf("getFWVersion: %v", err)
	}
	modemFW, err := getModemFirmware()
	if err != nil {
		logf("getModemFirmware: %v", err)
	}
	iccid, err := getICCID()
	if err != nil {
		logf("getICCID: %v", err)
	}
	netOperator, err := getNetOperator()
	if err != nil {
		logf("getNetOperator: %v", err)
	}

	return modules.DeviceInfo{
		DeviceID:    deviceIDFromIMEI(imei),
		FWName:      fwVersion,
		IMEI:        imei,
		SIMNo:       iccid,
		PubKey:      pubKey,
		NetOperator: netOperator,
		CommModule:  modemFW,
		APN:         apn,
	}
}

func main() {
	logf("starting: server=%s interval=%s", serverAddr, sendInterval)

	identity, err := beacon.LoadOrCreateIdentity(identityPath)
	if err != nil {
		logf("fatal: LoadOrCreateIdentity: %v", err)
		os.Exit(1)
	}
	pubKeyArr := identity.PublicKey()
	pubKey := pubKeyArr[:]

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// The login handshake's HostID is the device's own DEVICEID, derived
	// from IMEI, which shouldn't change - fetch it once up front.
	var hostID string
	for {
		imei, err := getIMEI()
		if err == nil {
			hostID = deviceIDFromIMEI(imei)
			break
		}
		logf("getIMEI (for login handshake): %v, retrying", err)
		select {
		case <-sigCh:
			return
		case <-time.After(5 * time.Second):
		}
	}

	var session *beacon.Session
	var seq uint16

	connectUntilStopped := func() bool {
		for {
			s, err := beacon.Dial(serverAddr, identity, hostID, 15*time.Second, queueDir)
			if err == nil {
				logf("connected to %s", serverAddr)
				session = s
				return true
			}
			logf("Dial(%s) failed: %v", serverAddr, err)
			select {
			case <-sigCh:
				return false
			case <-time.After(10 * time.Second):
			}
		}
	}

	if !connectUntilStopped() {
		return
	}
	defer session.Close()

	// GNSS is left running continuously (matching LAFV2's own EC25E handling
	// - src/comm/comm_ec25e/lafm_comm_ec25e_gnss.c - which enables the GNSS
	// engine and leaves it running, caching whatever fix arrives; the
	// beacon send path only ever reads that cache). Starting/stopping
	// navigation once per beacon cycle - the first version of this code
	// did - never gives the receiver enough continuous runtime to acquire
	// a fix. gnssWatchdogLoop only detects staleness and signals gnssStaleCh;
	// the actual recovery action (restartNavigation) is decided and run
	// here, not inside that background goroutine - see gnss_session.go's
	// package doc comment for why.
	var gnssStaleCh chan string
	if err := startGNSSSession(); err != nil {
		logf("startGNSSSession: %v", err)
	} else {
		defer stopGNSSSession()
		gnssWatchdogStop := make(chan struct{})
		defer close(gnssWatchdogStop)
		gnssStaleCh = make(chan string, 1)
		go gnssWatchdogLoop(gnssWatchdogStop, gnssStaleCh)
	}

	var lastTimestampUTCMs uint64
	sendOnce := func() {
		info := gatherDeviceInfo(pubKey)
		gnss := getGNSSInfo()
		io := getIOInfo()
		// LAFV2 sends this as the first module of every beacon (see
		// modules/timestamp.go) and nudges utctime by 1ms if it's identical
		// to the last beacon's, so the server never sees two beacons with
		// the same timestamp - mirrored here.
		utcMs := uint64(time.Now().UnixMilli())
		if utcMs == lastTimestampUTCMs {
			utcMs++
		}
		lastTimestampUTCMs = utcMs
		ts := modules.Timestamp{UptimeMs: getUptimeMs(), UTCTimeMs: utcMs}

		seq++
		inner := beacon.NewBeacon(beacon.TypeFromDevice, seq)
		inner.AddModule(beacon.ModuleTimestamp, ts.Encode())
		inner.AddModule(beacon.ModuleDeviceInfo, info.Encode())
		inner.AddModule(beacon.ModuleGNSSInfo, gnss.Encode())
		inner.AddModule(beacon.ModuleIOData, io.Encode())
		if err := session.SendBeacon(inner.Finish()); err != nil {
			logf("SendBeacon failed: %v, reconnecting", err)
			session.Close()
			connectUntilStopped()
			return
		}
		logf("sent beacon: deviceid=%q imei=%q iccid=%q operator=%q modemfw=%q fwversion=%q fixquality=%d nsatused=%d lat=%.6f lon=%.6f",
			info.DeviceID, info.IMEI, info.SIMNo, info.NetOperator, info.CommModule, info.FWName,
			gnss.FixQuality, gnss.NSatUsed, gnss.LatitudeDeg, gnss.LongitudeDeg)
	}

	ticker := time.NewTicker(sendInterval)
	defer ticker.Stop()

	sendOnce()
	for {
		select {
		case <-ticker.C:
			sendOnce()
		case acks, ok := <-session.Acks():
			if !ok {
				continue // connection's read side ended; SendBeacon will notice and reconnect
			}
			logf("server acked sequence IDs: %v (we're at seq %d)", acks, session.LastSeq())
		case seq, ok := <-session.Retries():
			if !ok {
				continue
			}
			logf("no ack for seq %d within timeout, resent it", seq)
		case reason := <-gnssStaleCh:
			// nil gnssStaleCh (startGNSSSession failed above) never fires this case.
			logf("GNSS watchdog: %s, restarting navigation", reason)
			if err := restartNavigation(); err != nil {
				logf("restartNavigation failed: %v", err)
			}
		case <-sigCh:
			logf("shutting down")
			return
		}
	}
}
