package associate

import (
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	pb "github.com/thinkaliker/labassistant/proto/v1"
)

// init primes the CPU sampler so the first gatherHealth has a baseline to diff against.
func init() { _, _ = cpu.Percent(0, false) }

// gatherHealth collects host vitals for the heartbeat. Individual failures are tolerated
// (the field stays zero). CPU percent is measured since the previous call, so the first
// heartbeat may read 0.
func gatherHealth() *pb.HostHealth {
	h := &pb.HostHealth{}
	if p, err := cpu.Percent(0, false); err == nil && len(p) > 0 {
		h.CpuPercent = round1(p[0])
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		h.MemPercent = round1(vm.UsedPercent)
		h.MemUsedBytes = vm.Used
		h.MemTotalBytes = vm.Total
	}
	if up, err := host.Uptime(); err == nil {
		h.UptimeSeconds = up
	}
	if parts, err := disk.Partitions(false); err == nil {
		seen := map[string]bool{}
		for _, pt := range parts {
			if seen[pt.Mountpoint] {
				continue
			}
			seen[pt.Mountpoint] = true
			if u, err := disk.Usage(pt.Mountpoint); err == nil {
				h.Disks = append(h.Disks, &pb.DiskUsage{
					Mount: pt.Mountpoint, TotalBytes: u.Total, UsedBytes: u.Used,
				})
			}
		}
	}
	return h
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
