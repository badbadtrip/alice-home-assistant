# Patched station-airplay v0.2.3

The bridge embeds this AirPlay 2 receiver. `go.mod` replaces the upstream module with
this copy. Upstream: https://github.com/skrashevich/station-airplay (Apache-2.0, see
LICENSE and NOTICE). Only Go sources needed to build and the module files are kept;
upstream tests, docs (except docs/licenses, which NOTICE refers to) and packaging
scripts are left out. Every change is marked
"Alice patch".

1. **Ducking under Alice's voice** (`audio/player.go`, `media.go`): `Receiver.SetDuck(f)`
   scales the receiver's PCM by `f` (0..1, 1 restores). The factor is applied where the
   receiver already applies its volume, right before writing to the output helper and
   after the AirPlay buffer, so it takes effect at once. Unlike `SetVolume` it is not
   reported to the sender: the phone's volume slider does not move.

Tests for the patch live in the bridge (`cmd/quasar-bridge`), which builds the real
receiver. To be offered upstream; drop this copy once a release has the same call.
