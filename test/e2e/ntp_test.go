package e2e

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// ntpPort is where the test's NTP server listens on the host. The nodes reach it at 10.0.2.2,
// the host's address on their user-mode network, as nix/testing/cluster.nix configures them; NTP's
// own port needs privileges the test has not.
const ntpPort = 12300

// startNTP answers NTP requests with the host's clock, as a stratum 1 server, until the test ends.
func startNTP(t *testing.T) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ntpPort})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 48 {
				continue
			}
			received := time.Now()
			conn.WriteToUDP(ntpResponse(buf[:48], received, time.Now()), addr)
		}
	}()
}

// ntpResponse answers a client's request (RFC 5905): the request's transmit time becomes the
// origin time, so the client matches the answer to its request.
func ntpResponse(request []byte, received, transmitted time.Time) []byte {
	r := make([]byte, 48)
	version := request[0] >> 3 & 7
	r[0] = version<<3 | 4                    // no leap second pending, server mode
	r[1] = 1                                 // stratum 1, a reference clock
	r[2] = request[2]                        // the client's poll interval
	r[3] = 0xec                              // precision of 2^-20 seconds
	binary.BigEndian.PutUint32(r[8:], 1<<10) // root dispersion of 1/64 seconds
	copy(r[12:], "GPS\x00")
	putNTPTime(r[16:], received)
	copy(r[24:32], request[40:48])
	putNTPTime(r[32:], received)
	putNTPTime(r[40:], transmitted)
	return r
}

// putNTPTime writes t as NTP's 64-bit timestamp: seconds since 1900 and a binary fraction.
func putNTPTime(b []byte, t time.Time) {
	const unixToNTP = 2208988800
	binary.BigEndian.PutUint32(b, uint32(t.Unix()+unixToNTP))
	binary.BigEndian.PutUint32(b[4:], uint32(uint64(t.Nanosecond())<<32/1e9))
}
