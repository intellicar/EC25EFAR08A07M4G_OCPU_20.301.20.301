#--------------------------------------------------------------
#               Quectel OpenLinux
#--------------------------------------------------------------
# Cross-compiles the ql_beacon Go daemon. Only the SIM/DM/NW device-info
# lookups need cgo against the vendor MCM libraries; the beacon protocol
# itself (framing/CRC/crypto) is pure Go. Dependencies are vendored so
# this builds without network access.
BIN         := ql_beacon
GO_EXTRA_LDLIBS := -lql_peripheral
export GOFLAGS := -mod=vendor

include ../go_common.mk


