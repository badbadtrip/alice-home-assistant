package zeroconf

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

// received packs a question as a sender would and unpacks it as the server does,
// so the name arrives in the escaped form miekg/dns produces.
func received(t *testing.T, name string, qtype uint16) dns.Question {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	wire, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	var m dns.Msg
	if err := m.Unpack(wire); err != nil {
		t.Fatal(err)
	}
	return m.Question[0]
}

func testServer() *Server {
	entry := NewServiceEntry("Алиса Комната", "_airplay._tcp", "local.")
	entry.Port = 7000
	entry.HostName = "Yandex-Station-Midi.local."
	entry.AddrIPv4 = []net.IP{net.IPv4(192, 168, 1, 59)}
	entry.Text = []string{"model=StationAirPlay"}
	return &Server{service: entry, ttl: 3200}
}

func answer(t *testing.T, s *Server, q dns.Question) *dns.Msg {
	t.Helper()
	resp := &dns.Msg{}
	if err := s.handleQuestion(q, resp, &dns.Msg{}, 0); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestInstanceQueryWithCyrillicAndSpace(t *testing.T) {
	s := testServer()
	q := received(t, s.service.ServiceInstanceName(), dns.TypeSRV)
	if q.Name == s.service.ServiceInstanceName() {
		t.Fatalf("test needs the escaped form, got %q", q.Name)
	}
	resp := answer(t, s, q)
	var srv, txt bool
	for _, rr := range resp.Answer {
		switch rr.Header().Rrtype {
		case dns.TypeSRV:
			srv = true
		case dns.TypeTXT:
			txt = true
		}
	}
	if !srv || !txt {
		t.Fatalf("instance query unanswered: %v", resp.Answer)
	}
}

func TestHostAddressQuery(t *testing.T) {
	s := testServer()
	resp := answer(t, s, received(t, "yandex-station-midi.local.", dns.TypeA))
	if len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "192.168.1.59" {
		t.Fatalf("host query: %v", resp.Answer)
	}
	if resp := answer(t, s, received(t, "yandex-station-midi.local.", dns.TypeAAAA)); len(resp.Answer) != 0 {
		t.Fatalf("no IPv6 address, got %v", resp.Answer)
	}
}

func TestBrowseStillAnswered(t *testing.T) {
	s := testServer()
	if resp := answer(t, s, received(t, "_airplay._tcp.local.", dns.TypePTR)); len(resp.Answer) == 0 {
		t.Fatal("browse unanswered")
	}
	if resp := answer(t, s, received(t, "Другая колонка._airplay._tcp.local.", dns.TypeSRV)); len(resp.Answer) != 0 {
		t.Fatalf("answered for another instance: %v", resp.Answer)
	}
}

func TestSameName(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"Алиса Комната._airplay._tcp.local.", `\208\144\208\187\208\184\209\129\208\176\ \208\154\208\190\208\188\208\189\208\176\209\130\208\176._airplay._tcp.local.`, true},
		{"Host.local.", "host.local", true},
		{"Алиса Кухня._airplay._tcp.local.", "Алиса Комната._airplay._tcp.local.", false},
	} {
		if got := sameName(c.a, c.b); got != c.same {
			t.Errorf("sameName(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
