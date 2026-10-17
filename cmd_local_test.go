package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return &net.IPNet{IP: ip.To4(), Mask: ipnet.Mask}
}

func TestNarrowSubnet(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"192.168.86.40/24", "192.168.86.0/24"},
		{"10.0.5.9/22", "10.0.4.0/22"},
		{"172.17.3.1/16", "172.17.3.0/24"},
	}
	for _, tt := range tests {
		if got := narrowSubnet(mustCIDR(t, tt.addr)).String(); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.addr, got, tt.want)
		}
	}
}

func TestSubnetHosts(t *testing.T) {
	if got, want := subnetHosts(mustCIDR(t, "192.168.1.0/30")), []string{"192.168.1.1", "192.168.1.2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("/30: got %v, want %v", got, want)
	}
	if got := subnetHosts(mustCIDR(t, "192.168.1.0/24")); len(got) != 254 || got[0] != "192.168.1.1" || got[253] != "192.168.1.254" {
		t.Errorf("/24: got %d hosts, %s to %s", len(got), got[0], got[len(got)-1])
	}
	if got, want := subnetHosts(mustCIDR(t, "192.168.1.7/32")), []string{"192.168.1.7"}; !reflect.DeepEqual(got, want) {
		t.Errorf("/32: got %v, want %v", got, want)
	}
}

func TestSortUnitsIsNumeric(t *testing.T) {
	units := sortUnits(map[string]LocalUnit{
		"192.168.1.100":     {Address: "192.168.1.100"},
		"192.168.1.9":       {Address: "192.168.1.9"},
		"192.168.1.20:4000": {Address: "192.168.1.20:4000"},
	})
	var got []string
	for _, u := range units {
		got = append(got, u.Address)
	}
	if want := []string{"192.168.1.9", "192.168.1.20:4000", "192.168.1.100"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// dnsName encodes a dotted name as DNS labels.
func dnsName(name string) []byte {
	return mdnsQuery(name)[12 : len(mdnsQuery(name))-4]
}

func dnsRecord(name []byte, rtype byte, rdata []byte) []byte {
	rr := append(append([]byte{}, name...), 0, rtype, 0, 1, 0, 0, 0, 120, 0, byte(len(rdata)))
	return append(rr, rdata...)
}

func TestParseMDNSResponse(t *testing.T) {
	// The shape avahi answers with: the PTR as the answer, then SRV and TXT whose names point back into it.
	msg := []byte{0, 1, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 2}
	msg = append(msg, dnsRecord(dnsName(mdnsService), 12, dnsName("SolarAssistant-42."+mdnsService))...)
	pointer := []byte{0xc0, 12}
	msg = append(msg, dnsRecord(pointer, 33, append([]byte{0, 0, 0, 0, 0x0f, 0xa0}, dnsName("unit.local")...))...)
	msg = append(msg, dnsRecord(pointer, 16, []byte("\x0asite_id=42\x15build_date=2026-03-01\x04flag"))...)

	port, txt, ok := parseMDNSResponse(msg)
	want := map[string]string{"site_id": "42", "build_date": "2026-03-01"}
	if !ok || port != 4000 || !reflect.DeepEqual(txt, want) {
		t.Errorf("got port %d txt %v ok %v", port, txt, ok)
	}
}

func TestParseMDNSResponseRejects(t *testing.T) {
	query := mdnsQuery(mdnsService)
	answer := append([]byte{0, 1, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0}, dnsRecord(dnsName(mdnsService), 12, dnsName("x.local"))...)
	truncated := append([]byte{0, 1, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0}, 5, 'a', 'b')
	for name, msg := range map[string][]byte{"a query": query, "no SRV record": answer, "truncated": truncated, "empty": nil} {
		if _, _, ok := parseMDNSResponse(msg); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func serveUnit(t *testing.T, paths map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := paths[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

const discoverSVG = `<svg version="1.1" xmlns="http://www.w3.org/2000/svg" height="100" width="100"><circle cx="50" cy="50" r="40" stroke-width="3" fill="green" /></svg>`

func TestProbeUnit(t *testing.T) {
	current := serveUnit(t, map[string]string{
		"/api/v1/discover.json": `{"site_id":42,"build_date":"2026-10-12"}`,
		"/api/v1/discover.svg":  discoverSVG,
	})
	if u, err := probeUnit(current); err != nil || u.Legacy || u.SiteID == nil || *u.SiteID != 42 {
		t.Errorf("current build: got %+v, %v", u, err)
	}

	legacy := serveUnit(t, map[string]string{"/api/v1/discover.svg": discoverSVG})
	if u, err := probeUnit(legacy); err != nil || !u.Legacy || u.SiteID != nil {
		t.Errorf("legacy build: got %+v, %v", u, err)
	}

	other := serveUnit(t, map[string]string{
		"/api/v1/discover.json": `{"status":"ok"}`,
		"/api/v1/discover.svg":  "<svg></svg>",
	})
	if u, err := probeUnit(other); err == nil {
		t.Errorf("not a unit: got %+v", u)
	}
}
