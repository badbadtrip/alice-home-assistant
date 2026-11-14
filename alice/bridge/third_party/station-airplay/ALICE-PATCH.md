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
2. **Diagnostics for resume after pause** (`handlers/play.go`, `audio/server.go`): logs the
   sender's `FLUSHBUFFERED` fields (`flushFromSeq/TS`, `flushUntilSeq/TS`), the buffer at
   each rate anchor, and the first frame kept after a flush or anchor with the counts
   dropped as flushed or as earlier than the anchor. On 2026-11-14 a resume after a voice
   pause waited 8.6 s for audio (and once 2 h 33 min on a stale anchor); these lines show
   which frames were thrown away. Logging only, no behaviour change.

Tests for the patch live in the bridge (`cmd/quasar-bridge`), which builds the real
receiver. To be offered upstream; drop this copy once a release has the same call.
