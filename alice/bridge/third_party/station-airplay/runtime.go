package stationairplay

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/skrashevich/station-airplay/audio"
	"github.com/skrashevich/station-airplay/codec"
	"github.com/skrashevich/station-airplay/config"
	"github.com/skrashevich/station-airplay/handlers"
	"github.com/skrashevich/station-airplay/homekit"
	"github.com/skrashevich/station-airplay/internal/pcmbundle"
	"github.com/skrashevich/station-airplay/ptp"
	"github.com/skrashevich/station-airplay/rtsp"
)

// CheckResult describes prerequisites without opening listeners or identity files.
// It may create or repair the private PCM helper cache in DataDir.
// Checking the owned output loads ALSA in a child process without opening a PCM.
type CheckResult struct {
	Interface, IP, Aplay, Device, DataDir string
	outputEnv                             []string
}

func (r *Receiver) Check(ctx context.Context) (CheckResult, error) {
	_, result, err := environment(ctx, r.config)
	return result, err
}

func environment(ctx context.Context, s settings) (net.Interface, CheckResult, error) {
	var result CheckResult
	if err := ctx.Err(); err != nil {
		return net.Interface{}, result, err
	}
	var command string
	var outputEnv []string
	var err error
	if s.ownedOutput {
		command, outputEnv, err = pcmbundle.Resolve(ctx, s.aplay, s.state)
		if err != nil {
			return net.Interface{}, result, err
		}
		if err = checkOutput(ctx, command, outputEnv); err != nil {
			return net.Interface{}, result, err
		}
	} else {
		command, err = exec.LookPath(s.aplay)
		if err != nil {
			return net.Interface{}, result, fmt.Errorf("audio output command is required: %w", err)
		}
	}
	iface, ip, err := selectInterface(s.iface)
	if err != nil {
		return net.Interface{}, result, err
	}
	result = CheckResult{Interface: iface.Name, IP: ip.String(), Aplay: command, Device: s.device, DataDir: s.state, outputEnv: outputEnv}
	return iface, result, ctx.Err()
}

// Only the bundled helper implements --check. It resolves the ALSA API without
// opening a PCM; vendor dlopen constructors may still initialize library state.
func checkOutput(ctx context.Context, command string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, command, "--check")
	process.Env = append(os.Environ(), env...)
	process.WaitDelay = time.Second
	output, err := process.CombinedOutput()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("bundled PCM output check failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type protocolService struct {
	settings        settings
	control, events net.Listener
	timing          *ptp.Server
	mdns, raop      *zeroconf.Server
	hap             *homekit.Server
	accessory       *homekit.Accessory
	player          *audio.Player
	once            sync.Once
	pausedMu        sync.Mutex
	pausedRemote    *pausedRemote
}

func openService(ctx context.Context, cfg settings) (_ service, failure error) {
	iface, env, err := environment(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s := &protocolService{settings: cfg}
	s.settings.aplay = env.Aplay
	defer func() {
		if failure != nil {
			s.Close()
		}
	}()
	id, err := config.LoadIdentity(cfg.state)
	if err != nil {
		return nil, err
	}
	s.hap, err = homekit.NewServer(id.DeviceID, cfg.state, []string{env.IP})
	if err != nil {
		return nil, err
	}
	s.hap.Name = cfg.name
	s.accessory = homekit.NewAccessory(id.DeviceID, id.UUID, airplayDevice(), s.hap.Device.PublicKey())
	host, port, _ := net.SplitHostPort(cfg.listen)
	if host == "" {
		host = env.IP
	}
	lc := new(net.ListenConfig)
	s.control, err = lc.Listen(ctx, "tcp4", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("RTSP listen: %w", err)
	}
	s.events, err = lc.Listen(ctx, "tcp4", net.JoinHostPort(env.IP, "0"))
	if err != nil {
		return nil, fmt.Errorf("event listen: %w", err)
	}
	s.hap.EventPort = uint16(s.events.Addr().(*net.TCPAddr).Port)
	clock := ptp.NewVirtualClock(0)
	s.timing, err = ptp.Listen(clock, net.ParseIP(env.IP), id.ClockID())
	if err != nil {
		return nil, err
	}
	s.timing.Logger = cfg.logger
	s.player = audio.NewPlayer(ctx, clock, codec.OutputOptions{Command: env.Aplay, Device: cfg.device, Env: env.outputEnv})
	s.player.Logger = cfg.logger
	s.player.BindIP, s.player.Timing, s.player.OutputDelay = env.IP, s.timing, cfg.delay
	s.player.SetVolume(cfg.volume)
	actualPort := s.control.Addr().(*net.TCPAddr).Port
	s.mdns, err = zeroconf.Register(cfg.name, "_airplay._tcp", "local.", actualPort, s.accessory.ToRecords(), []net.Interface{iface})
	if err != nil {
		return nil, fmt.Errorf("mDNS: %w", err)
	}
	txt := []string{"ch=2", "cn=1,2", "et=4", "sr=44100", "ss=16", "tp=TCP,UDP", "vn=65537", "vs=366.0",
		"am=StationAirPlay", "sf=0x4", "ft=" + airplayDevice().ToRecord(), "pk=" + hex.EncodeToString(s.hap.Device.PublicKey())}
	s.raop, err = zeroconf.Register(strings.ReplaceAll(id.DeviceID, ":", "")+"@"+cfg.name, "_raop._tcp", "local.", actualPort, txt, []net.Interface{iface})
	if err != nil {
		return nil, fmt.Errorf("RAOP mDNS: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *protocolService) Status() Status {
	connected, playing := s.player.PlaybackState()
	paused := s.pausedSender()
	connected = connected || paused != nil
	return Status{Name: s.settings.name, Address: s.control.Addr().String(), Connected: connected, Playing: playing, VolumeDB: s.player.Volume()}
}

func (s *protocolService) Serve(ctx context.Context) error {
	local, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	results := make(chan error, 3)
	wg.Go(func() { results <- s.hap.Events.Serve(local, s.events) })
	wg.Go(func() { results <- s.timing.Serve(local) })
	wg.Go(func() {
		results <- rtsp.Serve(local, s.control, func() (rtsp.Handler, error) {
			return handlers.NewRstpHandler(s.hap, s.accessory, s.player)
		}, s.settings.logger)
	})
	s.settings.logger.Printf("ready name=%q interface=%s address=%s event=%d codec=AAC-LC,ALAC output=%s device=%s state=%s",
		s.settings.name, s.settings.iface, s.control.Addr(), s.hap.EventPort, s.settings.aplay, s.settings.device, s.settings.state)
	err := <-results
	cancel()
	s.Close()
	wg.Wait()
	return err
}

func (s *protocolService) Close() {
	s.once.Do(func() {
		if s.raop != nil {
			s.raop.Shutdown()
		}
		if s.mdns != nil {
			s.mdns.Shutdown()
		}
		if s.control != nil {
			s.control.Close()
		}
		if s.events != nil {
			s.events.Close()
		}
		if s.timing != nil {
			s.timing.Close()
		}
	})
}
