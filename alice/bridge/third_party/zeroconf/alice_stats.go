package zeroconf

// Alice patch: diagnostics. Counts the questions about our service and the
// answers sent, so the log shows whether a sender's queries reach the speaker
// while it is missing from the sender's AirPlay list.

import (
	"github.com/miekg/dns"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type queryStats struct {
	mu         sync.Mutex
	packets    int
	questions  int // questions about our names
	multicast  int
	unicast    int
	suppressed int // known answers, nothing sent
	kinds      map[string]int
	senders    map[string]int
}

func (st *queryStats) packet() {
	st.mu.Lock()
	st.packets++
	st.mu.Unlock()
}

func (st *queryStats) question(kind string, from net.Addr, answered, unicast bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.kinds == nil {
		st.kinds = map[string]int{}
		st.senders = map[string]int{}
	}
	st.questions++
	st.kinds[kind]++
	if addr, ok := from.(*net.UDPAddr); ok {
		st.senders[addr.IP.String()]++
	}
	switch {
	case !answered:
		st.suppressed++
	case unicast:
		st.unicast++
	default:
		st.multicast++
	}
}

// flush logs and resets the counters. Logged even when nothing matched, so a
// silent minute is visible too.
func (st *queryStats) flush(name string, rejoined bool) {
	st.mu.Lock()
	packets, questions, multicast, unicast, suppressed := st.packets, st.questions, st.multicast, st.unicast, st.suppressed
	kinds, senders := joinCounts(st.kinds), joinCounts(st.senders)
	st.packets, st.questions, st.multicast, st.unicast, st.suppressed = 0, 0, 0, 0, 0
	st.kinds, st.senders = nil, nil
	st.mu.Unlock()
	slog.Info("mdns_stats", "name", name, "packets", packets, "questions", questions,
		"multicast", multicast, "unicast", unicast, "suppressed", suppressed,
		"kinds", kinds, "senders", senders, "rejoined", rejoined)
}

func joinCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Itoa(m[k]))
	}
	return strings.Join(parts, ",")
}

// questionKind names a question about our service, or "" for other names.
func (s *Server) questionKind(q dns.Question) string {
	if s.service == nil {
		return ""
	}
	switch {
	case sameName(q.Name, s.service.ServiceTypeName()):
		return "types"
	case sameName(q.Name, s.service.ServiceName()):
		return "browse"
	case sameName(q.Name, s.service.ServiceInstanceName()):
		return "instance"
	case sameName(q.Name, s.service.HostName):
		return "host"
	}
	return ""
}
