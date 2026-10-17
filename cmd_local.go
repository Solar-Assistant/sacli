package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/Solar-Assistant/go_solar_assistant/cloud"
)

const mdnsService = "_solar-assistant._tcp.local"

// A unit that is not listening refuses at once, so this only bounds addresses nothing answers on.
var localProbeClient = &http.Client{
	Timeout:   3 * time.Second,
	Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 1500 * time.Millisecond}).DialContext},
}

// LocalUnit is one SolarAssistant unit found on the local network.
type LocalUnit struct {
	Address        string `json:"address"`
	SiteID         *int   `json:"site_id"`
	OrganizationID *int   `json:"organization_id"`
	MachineName    string `json:"machine_name"`
	BuildDate      string `json:"build_date"`
	SiteURL        string `json:"site_url"`
	Ethernet       string `json:"ethernet"`
	Wifi           string `json:"wifi"`
	Monitoring     string `json:"monitoring"`
	// Legacy is a build from before 2026-03-24, which answers only discover.svg and so cannot say which unit it is.
	Legacy bool `json:"legacy,omitempty"`
}

func runLocal(args []string) error {
	jsonOut, args := extractFlag(args, "--json")
	scan, args := extractFlag(args, "--scan")

	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println(`Usage: sacli local [--json] [--scan] [cidr ...]

List the SolarAssistant units on the local network.

The cloud is asked which units share this network's internet connection, and each one
that answers at its local address is listed. That includes a unit not yet registered,
once it has been online.

  --scan    Search the network itself instead: mDNS, and a request to every address on
            this machine's subnets. Needs no API key or internet, and is what runs when
            no API key is configured. A subnet larger than /22 is narrowed to the /24
            around this machine's own address; name a range to search somewhere else.

An address from the list works wherever a site does, for a unit your API key can reach:
  sacli site 192.168.1.50 metrics
  sacli ssh 192.168.1.50

Examples:
  sacli local
  sacli local --json
  sacli local --scan
  sacli local 10.0.20.0/24`)
		return nil
	}

	units, err := findLocalUnits(args, scan)
	if err != nil {
		return err
	}

	if jsonOut {
		out, _ := json.MarshalIndent(units, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	if len(units) == 0 {
		fmt.Println("No units found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ADDRESS\tSITE ID\tNAME\tBUILD DATE\tETHERNET\tWIFI\tMONITORING")
	for _, u := range units {
		site := "not registered"
		if u.SiteID != nil && *u.SiteID != 0 {
			site = strconv.Itoa(*u.SiteID)
		} else if u.Legacy {
			site = "unknown"
		}
		if u.Legacy && u.BuildDate == "" {
			u.BuildDate = "before 2026-03-24"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", u.Address, site, strOr(u.MachineName, "-"),
			strOr(u.BuildDate, "-"), strOr(u.Ethernet, "-"), strOr(u.Wifi, "-"), strOr(u.Monitoring, "-"))
	}
	return w.Flush()
}

// findLocalUnits lists the units the cloud places on this network, or searches the network itself
// when scan is set, ranges are given, or the cloud cannot be asked.
func findLocalUnits(cidrs []string, scan bool) ([]LocalUnit, error) {
	if len(cidrs) > 0 {
		var hosts []string
		for _, c := range cidrs {
			_, ipnet, err := net.ParseCIDR(c)
			if err != nil || ipnet.IP.To4() == nil {
				return nil, fmt.Errorf("invalid IPv4 range %q — expected e.g. 192.168.1.0/24", c)
			}
			if ones, _ := ipnet.Mask.Size(); ones < 16 {
				return nil, fmt.Errorf("range %s is too large to search — use /16 or smaller", c)
			}
			hosts = append(hosts, subnetHosts(ipnet)...)
		}
		return sortUnits(probeHosts(hosts)), nil
	}
	if !scan {
		if client, err := newClient(); err == nil {
			units, err := cloudLocalUnits(client)
			if err == nil {
				return units, nil
			}
			fmt.Fprintf(os.Stderr, "warning: could not ask the cloud (%v), scanning the network instead\n", err)
		}
	}
	return scanLocalUnits(), nil
}

// cloudLocalUnits asks the cloud for the sites behind this network's internet address and keeps those that answer locally.
func cloudLocalUnits(client *cloud.Client) ([]LocalUnit, error) {
	body, err := client.Get("/api/v1/sites/local", nil)
	if err != nil {
		return nil, err
	}
	var sites []struct {
		ID      int     `json:"id"`
		Name    *string `json:"name"`
		LocalIP string  `json:"local_ip"`
	}
	if err := json.Unmarshal(body, &sites); err != nil {
		return nil, fmt.Errorf("unexpected response: %w", err)
	}
	expected := map[string][]int{}
	var hosts []string
	for _, site := range sites {
		if ip := net.ParseIP(site.LocalIP); ip == nil || ip.To4() == nil {
			continue
		}
		expected[site.LocalIP] = append(expected[site.LocalIP], site.ID)
		// An unregistered unit may report its cloud id or none at all, depending on its build.
		if site.Name == nil {
			expected[site.LocalIP] = append(expected[site.LocalIP], 0)
		}
		hosts = append(hosts, site.LocalIP)
	}
	found := probeHosts(hosts)
	// The cloud's local address goes stale when DHCP hands it to another unit, so the unit has to agree on who it is.
	for addr, u := range found {
		id := 0
		if u.SiteID != nil {
			id = *u.SiteID
		}
		// A legacy unit cannot say, so it is listed without the cloud's id rather than risk the wrong one.
		if !u.Legacy && !slices.Contains(expected[addr], id) {
			delete(found, addr)
		}
	}
	return sortUnits(found), nil
}

// scanLocalUnits searches this machine's own subnets and mDNS.
func scanLocalUnits() []LocalUnit {
	addrs := localIPv4Addrs()
	browsed := make(chan map[string]LocalUnit, 1)
	go func() { browsed <- mdnsBrowse(addrs, 1500*time.Millisecond) }()
	var hosts []string
	for _, a := range addrs {
		hosts = append(hosts, subnetHosts(narrowSubnet(a))...)
	}
	found := probeHosts(hosts)
	advertised := <-browsed

	// A unit advertising on a port other than 80, or on a subnet the probe narrowed away.
	var extra []string
	for addr := range advertised {
		if _, ok := found[addr]; !ok {
			extra = append(extra, addr)
		}
	}
	for addr, u := range probeHosts(extra) {
		found[addr] = u
	}
	// A build too old to serve discover.json may still advertise what it is.
	for addr, u := range advertised {
		if f, ok := found[addr]; !ok || f.Legacy {
			u.Legacy = ok
			found[addr] = u
		}
	}
	return sortUnits(found)
}

func sortUnits(found map[string]LocalUnit) []LocalUnit {
	units := make([]LocalUnit, 0, len(found))
	for _, u := range found {
		units = append(units, u)
	}
	sort.Slice(units, func(i, j int) bool {
		a, b := net.ParseIP(hostOnly(units[i].Address)).To4(), net.ParseIP(hostOnly(units[j].Address)).To4()
		if a == nil || b == nil || a.Equal(b) {
			return units[i].Address < units[j].Address
		}
		return binary.BigEndian.Uint32(a) < binary.BigEndian.Uint32(b)
	})
	return units
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// localIPv4Addrs returns the IPv4 address and mask of every interface that is up and not loopback.
func localIPv4Addrs() []*net.IPNet {
	var out []*net.IPNet
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				out = append(out, &net.IPNet{IP: ipnet.IP.To4(), Mask: ipnet.Mask})
			}
		}
	}
	return out
}

// lastOctetAddr returns the address on this machine's network that ends in s, when s is a bare number that could be one.
func lastOctetAddr(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 254 || strconv.Itoa(n) != s {
		return ""
	}
	for _, a := range localIPv4Addrs() {
		if ones, _ := a.Mask.Size(); ones == 24 {
			return fmt.Sprintf("%d.%d.%d.%d", a.IP[0], a.IP[1], a.IP[2], n)
		}
	}
	return ""
}

// narrowSubnet keeps a subnet of up to 1022 hosts whole and cuts a larger one to the /24 around addr.
func narrowSubnet(addr *net.IPNet) *net.IPNet {
	mask := addr.Mask
	if ones, _ := mask.Size(); ones < 22 {
		mask = net.CIDRMask(24, 32)
	}
	return &net.IPNet{IP: addr.IP.Mask(mask), Mask: mask}
}

// subnetHosts lists the host addresses in a subnet, leaving out the network and broadcast addresses.
func subnetHosts(subnet *net.IPNet) []string {
	ones, bits := subnet.Mask.Size()
	base := binary.BigEndian.Uint32(subnet.IP.Mask(subnet.Mask).To4())
	size := uint32(1) << (bits - ones)
	if size <= 2 {
		return []string{subnet.IP.String()}
	}
	hosts := make([]string, 0, size-2)
	for i := uint32(1); i < size-1; i++ {
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, base+i)
		hosts = append(hosts, ip.String())
	}
	return hosts
}

// probeHosts asks each host for discover.json and returns the ones that answered as a unit, keyed by address.
func probeHosts(hosts []string) map[string]LocalUnit {
	found := map[string]LocalUnit{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 1024)
	seen := map[string]bool{}
	for _, host := range hosts {
		if seen[host] {
			continue
		}
		seen[host] = true
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if u, err := probeUnit(host); err == nil {
				mu.Lock()
				found[host] = *u
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return found
}

// probeUnit identifies the unit at host by discover.json, or recognises a legacy one by discover.svg.
func probeUnit(host string) (*LocalUnit, error) {
	u, err := discoverUnit(host)
	if err == nil {
		return u, nil
	}
	if !answersDiscoverSVG(host) {
		return nil, err
	}
	return &LocalUnit{Address: host, Legacy: true}, nil
}

// answersDiscoverSVG reports whether host serves the green-circle liveness image every build has served.
func answersDiscoverSVG(host string) bool {
	resp, err := localProbeClient.Get("http://" + host + "/api/v1/discover.svg")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return err == nil && resp.StatusCode == http.StatusOK &&
		strings.Contains(string(body), "<svg") && strings.Contains(string(body), `fill="green"`)
}

// discoverUnit reads a unit's unauthenticated /api/v1/discover.json.
func discoverUnit(host string) (*LocalUnit, error) {
	resp, err := localProbeClient.Get("http://" + host + "/api/v1/discover.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	// Anything on the LAN may answer this path with JSON; only a unit names its build.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	if _, ok := fields["build_date"]; !ok {
		return nil, fmt.Errorf("not a SolarAssistant unit")
	}
	u := LocalUnit{}
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, err
	}
	u.Address = host
	return &u, nil
}

// authorizeLocalUnit gets cloud auth for the registered unit at host, so a LAN address needs no password of its own.
func authorizeLocalUnit(host string, roles ...string) (CachedAuthorize, error) {
	unit, err := discoverUnit(host)
	if err != nil {
		if answersDiscoverSVG(host) {
			return CachedAuthorize{}, fmt.Errorf("the unit at %s runs a build from before 2026-03-24, which cannot say which site it is", host)
		}
		return CachedAuthorize{}, fmt.Errorf("no SolarAssistant unit answered at %s: %w", host, err)
	}
	if unit.SiteID == nil || *unit.SiteID == 0 {
		return CachedAuthorize{}, fmt.Errorf("the unit at %s is not registered to a site, so an API key cannot authorize it", host)
	}
	auth, err := authorizeWithCache(*unit.SiteID, roles...)
	if err != nil {
		return CachedAuthorize{}, fmt.Errorf("the unit at %s is site %d: %w", host, *unit.SiteID, err)
	}
	auth.LocalIP = host
	return auth, nil
}

// ── mDNS ──────────────────────────────────────────────────────────────────────

// mdnsBrowse asks every interface for the SolarAssistant service and returns the units that answered, keyed by address.
func mdnsBrowse(addrs []*net.IPNet, wait time.Duration) map[string]LocalUnit {
	found := map[string]LocalUnit{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	query := mdnsQuery(mdnsService)
	for _, a := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Binding to the interface's address is what sends the query out of that interface.
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: a.IP})
			if err != nil {
				return
			}
			defer conn.Close()
			if _, err := conn.WriteToUDP(query, &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}); err != nil {
				return
			}
			conn.SetReadDeadline(time.Now().Add(wait))
			buf := make([]byte, 9000)
			for {
				n, from, err := conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				port, txt, ok := parseMDNSResponse(buf[:n])
				if !ok {
					continue
				}
				addr := from.IP.String()
				if port != 80 {
					addr = net.JoinHostPort(addr, strconv.Itoa(port))
				}
				u := LocalUnit{Address: addr, BuildDate: txt["build_date"], SiteURL: txt["site_url"]}
				if id, err := strconv.Atoi(txt["site_id"]); err == nil {
					u.SiteID = &id
				}
				mu.Lock()
				found[addr] = u
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return found
}

// mdnsQuery builds a one-question PTR query for name.
func mdnsQuery(name string) []byte {
	// Sent from an ephemeral port, so responders answer it by unicast and no group membership is needed.
	msg := []byte{0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(name, ".") {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	return append(msg, 0, 0, 12, 0, 1)
}

// parseMDNSResponse pulls the service port and TXT entries out of a DNS response; ok is false without an SRV record.
func parseMDNSResponse(msg []byte) (port int, txt map[string]string, ok bool) {
	if len(msg) < 12 || msg[2]&0x80 == 0 {
		return 0, nil, false
	}
	questions := int(binary.BigEndian.Uint16(msg[4:]))
	records := int(binary.BigEndian.Uint16(msg[6:])) + int(binary.BigEndian.Uint16(msg[8:])) + int(binary.BigEndian.Uint16(msg[10:]))
	txt = map[string]string{}
	off := 12
	for i := 0; i < questions; i++ {
		if off = skipDNSName(msg, off); off < 0 || off+4 > len(msg) {
			return 0, nil, false
		}
		off += 4
	}
	for i := 0; i < records; i++ {
		if off = skipDNSName(msg, off); off < 0 || off+10 > len(msg) {
			break
		}
		rtype := binary.BigEndian.Uint16(msg[off:])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8:]))
		off += 10
		if off+rdlen > len(msg) {
			break
		}
		rdata := msg[off : off+rdlen]
		off += rdlen
		switch rtype {
		case 33: // SRV
			if len(rdata) >= 6 {
				port = int(binary.BigEndian.Uint16(rdata[4:]))
				ok = true
			}
		case 16: // TXT
			for len(rdata) > 0 {
				n := int(rdata[0])
				if 1+n > len(rdata) {
					break
				}
				if k, v, found := strings.Cut(string(rdata[1:1+n]), "="); found {
					txt[k] = v
				}
				rdata = rdata[1+n:]
			}
		}
	}
	return port, txt, ok
}

// skipDNSName returns the offset just past the name at off, or -1 if it runs off the message.
func skipDNSName(msg []byte, off int) int {
	for off < len(msg) {
		n := int(msg[off])
		switch {
		case n == 0:
			return off + 1
		case n&0xc0 == 0xc0:
			return off + 2
		default:
			off += 1 + n
		}
	}
	return -1
}
