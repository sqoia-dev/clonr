package stats

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

// NetPlugin collects per-interface network statistics from /proc/net/dev.
//
// Sensors produced (label iface=<name>):
//   - "rx_bps"     — receive bytes/sec  (unit: bps)
//   - "tx_bps"     — transmit bytes/sec (unit: bps)
//   - "rx_packets" — receive packets/sec  (unit: count)
//   - "tx_packets" — transmit packets/sec (unit: count)
//   - "rx_errors"  — receive errors/sec   (unit: count)
//   - "tx_errors"  — transmit errors/sec  (unit: count)
type NetPlugin struct {
	prevStats  map[string]netStat
	prevTime   time.Time
	procNetDev string // injectable for tests
}

type netStat struct {
	rxBytes, rxPackets, rxErrors uint64
	txBytes, txPackets, txErrors uint64
}

// NewNetPlugin creates a NetPlugin reading /proc/net/dev.
func NewNetPlugin() *NetPlugin {
	return &NetPlugin{
		prevStats:  make(map[string]netStat),
		procNetDev: "/proc/net/dev",
	}
}

func (p *NetPlugin) Name() string { return "net" }

func (p *NetPlugin) Collect(_ context.Context) []Sample {
	now := time.Now().UTC()

	current, err := parseProcNetDev(p.procNetDev)
	if err != nil {
		return nil
	}

	var samples []Sample
	if !p.prevTime.IsZero() {
		elapsed := now.Sub(p.prevTime).Seconds()
		if elapsed > 0 {
			for iface, cur := range current {
				if iface == "lo" {
					continue // skip loopback
				}
				prev, ok := p.prevStats[iface]
				if !ok {
					continue
				}
				labels := map[string]string{"iface": iface}
				rxBPS := float64(cur.rxBytes-prev.rxBytes) / elapsed
				txBPS := float64(cur.txBytes-prev.txBytes) / elapsed
				rxPPS := float64(cur.rxPackets-prev.rxPackets) / elapsed
				txPPS := float64(cur.txPackets-prev.txPackets) / elapsed
				rxEPS := float64(cur.rxErrors-prev.rxErrors) / elapsed
				txEPS := float64(cur.txErrors-prev.txErrors) / elapsed
				samples = append(samples,
					Sample{Sensor: "rx_bps", Value: rxBPS, Unit: "bps", Labels: labels, TS: now},
					Sample{Sensor: "tx_bps", Value: txBPS, Unit: "bps", Labels: labels, TS: now},
					Sample{Sensor: "rx_packets", Value: rxPPS, Unit: "count", Labels: labels, TS: now},
					Sample{Sensor: "tx_packets", Value: txPPS, Unit: "count", Labels: labels, TS: now},
					Sample{Sensor: "rx_errors", Value: rxEPS, Unit: "count", Labels: labels, TS: now},
					Sample{Sensor: "tx_errors", Value: txEPS, Unit: "count", Labels: labels, TS: now},
				)
			}
		}
	}

	p.prevStats = current
	p.prevTime = now
	return samples
}

// parseProcNetDev reads /proc/net/dev and returns per-interface counters.
//
// /proc/net/dev format (2-line header then data):
//
//	Inter-|   Receive                                                |  Transmit
//	 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs ...
//	  eth0:  1234     56    0    0    0     0      0          0      789    10    0 ...
func parseProcNetDev(path string) (map[string]netStat, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]netStat)
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum <= 2 {
			continue // skip header lines
		}
		line := scanner.Text()
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colonIdx])
		fields := strings.Fields(line[colonIdx+1:])
		if len(fields) < 16 {
			continue
		}
		parse := func(s string) uint64 {
			v, _ := strconv.ParseUint(s, 10, 64)
			return v
		}
		result[iface] = netStat{
			rxBytes:   parse(fields[0]),
			rxPackets: parse(fields[1]),
			rxErrors:  parse(fields[2]),
			txBytes:   parse(fields[8]),
			txPackets: parse(fields[9]),
			txErrors:  parse(fields[10]),
		}
	}
	return result, scanner.Err()
}
