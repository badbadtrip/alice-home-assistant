package stationairplay

import "github.com/skrashevich/station-airplay/globals"

func airplayDevice() globals.Features {
	f := globals.NewFeatures()
	for _, bit := range []int{globals.SupportsAirPlayAudio, globals.HasUnifiedAdvertiserInfo, globals.SupportsBufferedAudio, globals.SupportsUnifiedMediaControl, globals.SupportsHKPairingAndAccessControl, globals.SupportsHKPeerManagement, globals.Authentication_4, globals.SupportsPTP, globals.AudioFormats_1, globals.AudioFormats_2, globals.SupportsCoreUtilsPairingAndEncryption} {
		f = f.Set(bit)
	}
	return f
}
