# Patched grandcat/zeroconf v1.0.0

The AirPlay receiver (station-airplay) announces itself with this library. `go.mod`
replaces the upstream module with this copy. Upstream: https://github.com/grandcat/zeroconf
(MIT, see LICENSE). Only `server.go` is changed; every change is marked "Alice patch".

1. **Names with non-ASCII letters or spaces.** A received question arrives escaped by
   miekg/dns (`\208\144\208\187…\ `), the service keeps raw UTF-8. Upstream compared the
   strings, so a direct query for «Алиса Комната._airplay._tcp.local.» was never answered.
   Names are now compared in wire form.
2. **Host address queries.** Upstream never answered `A`/`AAAA` for its own host name.
   These records live 120 s; a sender that cannot refresh them drops the receiver.
3. **Lost multicast membership.** Every 30 s the server joins the mDNS group again; the
   join succeeds only if membership was lost (Wi-Fi reconnect), and then the service is
   announced at once.

Together 1 and 2 made speakers vanish from the iPhone AirPlay list some time after the
receiver started, until it was restarted (measured 2026-11-14). A changed IP address is
handled by the bridge, which restarts the receiver (`followAddress` in `airplay.go`).

Tests: `go test github.com/grandcat/zeroconf` from `alice/bridge` (`alice_patch_test.go`).
