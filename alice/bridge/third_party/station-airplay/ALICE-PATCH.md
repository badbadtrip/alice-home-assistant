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
3. **Resume after pause without a hole** (`handlers/play.go`, `audio/server.go`). Measured
   with item 2: on pause the iPhone sends an immediate `FLUSHBUFFERED` (no `flushFromSeq`),
   starts a new RTP timeline at a random point and prebuffers into it; the resume anchor
   comes in the new timeline. The receiver judged the prebuffered frames against the old
   anchor, and when the new timeline started "below" it (int32 compare, about half the
   time) dropped them all as late: the sender does not resend them, so music came back
   ~8.6 s late. Now an immediate flush forgets the anchor, as shairport-sync does
   (`reset_ptp_anchor_info`). A new anchor keeps only frames 0–5 s after it, and if the
   first frame then arriving lies outside that window (anchor and audio in different
   timelines, which made the receiver "wait" 10 h 44 min), playback follows the audio
   with a 300 ms lead instead of waiting. Tests: `audio/alice_patch_test.go`, run with
   `go test github.com/skrashevich/station-airplay/audio` from `alice/bridge`.

4. **One volume with the speaker** (`options.go` `VolumeHooks`, `runtime.go`, `audio/player.go`,
   `handlers/parameter.go`, `media.go`). With `Config.Volume` set, the sender's slider
   (`SET_PARAMETER volume`) goes to the host instead of the receiver's gain, which stays at
   0 dB (only -144, silence, is applied locally); `GET_PARAMETER volume` answers with the
   host's volume so a connecting phone starts from it; `Receiver.ReportVolume` moves the
   sender's slider (`NotifyVolume`) without touching the gain. Without hooks nothing
   changes.

Tests for item 1 live in the bridge (`cmd/quasar-bridge`); item 3 in
`audio/alice_patch_test.go`, item 4 in `handlers/alice_patch_test.go`. To be offered upstream; drop this copy once a release has the same call.
