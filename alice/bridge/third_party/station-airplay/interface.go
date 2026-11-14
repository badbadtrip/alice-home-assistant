package stationairplay

import (
	"fmt"
	"net"
)

func selectInterface(name string) (net.Interface, net.IP, error) {
	list, err := net.Interfaces()
	if err != nil {
		return net.Interface{}, nil, err
	}
	var route net.IP
	if name == "" {
		if c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}); err == nil {
			route = c.LocalAddr().(*net.UDPAddr).IP
			c.Close()
		}
	}
	var fallback *net.Interface
	var fallbackIP net.IP
	for _, iface := range list {
		if name != "" && iface.Name != name {
			continue
		}
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil || ip.To4() == nil || !ip.IsGlobalUnicast() {
				continue
			}
			if name != "" || ip.Equal(route) {
				return iface, ip.To4(), nil
			}
			if fallback == nil {
				i := iface
				fallback = &i
				fallbackIP = ip.To4()
			}
		}
	}
	if fallback != nil {
		return *fallback, fallbackIP, nil
	}
	return net.Interface{}, nil, fmt.Errorf("no usable IPv4 multicast interface %q", name)
}
