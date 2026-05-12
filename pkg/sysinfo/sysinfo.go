package sysinfo

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/sensors"
)

type timestampJSON struct {
	time.Time
}

func (t timestampJSON) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(t.Unix(), 10)), nil
}

func (t *timestampJSON) UnmarshalJSON(data []byte) error {
	i, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return err
	}

	t.Time = time.Unix(i, 0)
	return nil
}

type SystemInfo struct {
	HostInfoIsAvailable bool          `json:"host_info_is_available"`
	BootTime            timestampJSON `json:"boot_time"`
	Hostname            string        `json:"hostname"`
	Platform            string        `json:"platform"`

	CPU struct {
		LoadIsAvailable bool  `json:"load_is_available"`
		Load1Percent    uint8 `json:"load1_percent"`
		Load15Percent   uint8 `json:"load15_percent"`

		TemperatureIsAvailable bool  `json:"temperature_is_available"`
		TemperatureC           uint8 `json:"temperature_c"`
	} `json:"cpu"`

	Memory struct {
		IsAvailable bool   `json:"memory_is_available"`
		TotalMB     uint64 `json:"total_mb"`
		UsedMB      uint64 `json:"used_mb"`
		UsedPercent uint8  `json:"used_percent"`

		SwapIsAvailable bool   `json:"swap_is_available"`
		SwapTotalMB     uint64 `json:"swap_total_mb"`
		SwapUsedMB      uint64 `json:"swap_used_mb"`
		SwapUsedPercent uint8  `json:"swap_used_percent"`
	} `json:"memory"`

	Mountpoints []MountpointInfo `json:"mountpoints"`
}

type MountpointInfo struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	TotalMB     uint64 `json:"total_mb"`
	UsedMB      uint64 `json:"used_mb"`
	UsedPercent uint8  `json:"used_percent"`
}

type SystemInfoRequest struct {
	CPUTempSensor            string                       `yaml:"cpu-temp-sensor"`
	HideMountpointsByDefault bool                         `yaml:"hide-mountpoints-by-default"`
	Mountpoints              map[string]MointpointRequest `yaml:"mountpoints"`
}

type MointpointRequest struct {
	Name string `yaml:"name"`
	Hide *bool  `yaml:"hide"`
}

// Currently caches hostname indefinitely which isn't ideal
// Potential issue with caching boot time as it may not initially get reported correctly:
// https://github.com/shirou/gopsutil/issues/842#issuecomment-1908972344
type cacheableHostInfo struct {
	available bool
	hostname  string
	platform  string
	bootTime  timestampJSON
}

var cachedHostInfo cacheableHostInfo

func getHostInfo() (cacheableHostInfo, error) {
	var err error
	info := cacheableHostInfo{}

	info.hostname, err = os.Hostname()
	if err != nil {
		return info, err
	}

	info.platform, _, _, err = host.PlatformInformation()
	if err != nil {
		return info, err
	}

	bootTime, err := host.BootTime()
	if err != nil {
		return info, err
	}

	info.bootTime = timestampJSON{time.Unix(int64(bootTime), 0)}
	info.available = true

	return info, nil
}

func Collect(req *SystemInfoRequest) (*SystemInfo, []error) {
	if req == nil {
		req = &SystemInfoRequest{}
	}

	var errs []error

	addErr := func(err error) {
		errs = append(errs, err)
	}

	info := &SystemInfo{
		Mountpoints: []MountpointInfo{},
	}

	applyCachedHostInfo := func() {
		info.HostInfoIsAvailable = true
		info.BootTime = cachedHostInfo.bootTime
		info.Hostname = cachedHostInfo.hostname
		info.Platform = cachedHostInfo.platform
	}

	if cachedHostInfo.available {
		applyCachedHostInfo()
	} else {
		hostInfo, err := getHostInfo()
		if err == nil {
			cachedHostInfo = hostInfo
			applyCachedHostInfo()
		} else {
			addErr(fmt.Errorf("getting host info: %v", err))
		}
	}

	coreCount, err := cpu.Counts(true)
	if err == nil {
		loadAvg, err := load.Avg()
		if err == nil {
			info.CPU.LoadIsAvailable = true
			if runtime.GOOS == "windows" {
				// The numbers returned here seem unreliable on Windows. Even with the CPU pegged
				// at close to 50% for multiple minutes, load1 is sometimes way under or way over
				// with no clear pattern. Dividing by core count gives numbers that are way too
				// low so that's likely not necessary as it is with unix.
				info.CPU.Load1Percent = uint8(math.Min(loadAvg.Load1*100, 100))
				info.CPU.Load15Percent = uint8(math.Min(loadAvg.Load15*100, 100))
			} else {
				info.CPU.Load1Percent = uint8(math.Min((loadAvg.Load1/float64(coreCount))*100, 100))
				info.CPU.Load15Percent = uint8(math.Min((loadAvg.Load15/float64(coreCount))*100, 100))
			}
		} else {
			addErr(fmt.Errorf("getting load avg: %v", err))
		}
	} else {
		addErr(fmt.Errorf("getting core count: %v", err))
	}

	memory, err := mem.VirtualMemory()
	if err == nil {
		info.Memory.IsAvailable = true
		info.Memory.TotalMB = memory.Total / 1024 / 1024
		info.Memory.UsedMB = memory.Used / 1024 / 1024
		info.Memory.UsedPercent = uint8(math.Min(memory.UsedPercent, 100))
	} else {
		addErr(fmt.Errorf("getting memory info: %v", err))
	}

	swapMemory, err := mem.SwapMemory()
	if err == nil {
		info.Memory.SwapIsAvailable = true
		info.Memory.SwapTotalMB = swapMemory.Total / 1024 / 1024
		info.Memory.SwapUsedMB = swapMemory.Used / 1024 / 1024
		info.Memory.SwapUsedPercent = uint8(math.Min(swapMemory.UsedPercent, 100))
	} else {
		addErr(fmt.Errorf("getting swap memory info: %v", err))
	}

	// currently disabled on Windows because it requires elevated privilidges, otherwise
	// keeps returning a single sensor with key "ACPI\\ThermalZone\\TZ00_0" which
	// doesn't seem to be the CPU sensor or correspond to anything useful when
	// compared against the temperatures Libre Hardware Monitor reports.
	// Also disabled on the bsd's because it's not implemented by go-psutil for them
	if runtime.GOOS != "windows" && runtime.GOOS != "openbsd" && runtime.GOOS != "netbsd" && runtime.GOOS != "freebsd" {
		sensorReadings, err := sensors.SensorsTemperatures()
		_, errIsWarning := err.(*sensors.Warnings)
		if err == nil || errIsWarning {
			if req.CPUTempSensor != "" {
				for i := range sensorReadings {
					if sensorReadings[i].SensorKey == req.CPUTempSensor {
						info.CPU.TemperatureIsAvailable = true
						info.CPU.TemperatureC = uint8(sensorReadings[i].Temperature)
						break
					}
				}

				if !info.CPU.TemperatureIsAvailable {
					addErr(fmt.Errorf("CPU temperature sensor %s not found", req.CPUTempSensor))
				}
			} else if cpuTempSensor := inferCPUTempSensor(sensorReadings); cpuTempSensor != nil {
				info.CPU.TemperatureIsAvailable = true
				info.CPU.TemperatureC = uint8(cpuTempSensor.Temperature)
			}
		} else {
			addErr(fmt.Errorf("getting sensor readings: %v", err))
		}
	}

	addedMountpoints := map[string]struct{}{}
	addMountpointInfo := func(requestedPath string, mpReq MointpointRequest) {
		if _, exists := addedMountpoints[requestedPath]; exists {
			return
		}

		isHidden := req.HideMountpointsByDefault
		if mpReq.Hide != nil {
			isHidden = *mpReq.Hide
		}
		if isHidden {
			return
		}

		usage, err := disk.Usage(requestedPath)
		if err == nil {
			totalMB := usage.Total / 1024 / 1024
			usedMB := usage.Used / 1024 / 1024
			usedPercent := uint8(math.Min(usage.UsedPercent, 100))

			// ZFS pool roots return Used=0 via statfs(2) when data is stored in child
			// datasets (e.g. TrueNAS SCALE). Override with accurate values from `zfs list`.
			if usage.Fstype == "zfs" {
				if zfsTotal, zfsUsed, zfsErr := getZFSUsage(requestedPath); zfsErr == nil {
					totalMB = zfsTotal / 1024 / 1024
					usedMB = zfsUsed / 1024 / 1024
					if zfsTotal > 0 {
						usedPercent = uint8(math.Min(float64(zfsUsed)/float64(zfsTotal)*100, 100))
					}
				}
			}

			mpInfo := MountpointInfo{
				Path:        requestedPath,
				Name:        mpReq.Name,
				TotalMB:     totalMB,
				UsedMB:      usedMB,
				UsedPercent: usedPercent,
			}

			info.Mountpoints = append(info.Mountpoints, mpInfo)
			addedMountpoints[requestedPath] = struct{}{}
		} else {
			addErr(fmt.Errorf("getting filesystem usage for %s: %v", requestedPath, err))
		}
	}

	if !req.HideMountpointsByDefault {
		filesystems, err := disk.Partitions(false)
		if err == nil {
			for _, fs := range filesystems {
				addMountpointInfo(fs.Mountpoint, req.Mountpoints[fs.Mountpoint])
			}
		} else {
			addErr(fmt.Errorf("getting filesystems: %v", err))
		}
	}

	for mountpoint, mpReq := range req.Mountpoints {
		addMountpointInfo(mountpoint, mpReq)
	}

	sort.Slice(info.Mountpoints, func(a, b int) bool {
		return info.Mountpoints[a].UsedPercent > info.Mountpoints[b].UsedPercent
	})

	return info, errs
}

// getZFSUsage returns accurate (totalBytes, usedBytes) for a ZFS mountpoint by
// invoking `zfs list`. This is necessary because statfs(2) on a ZFS pool root
// returns Used=0 when all data lives in child datasets (e.g. TrueNAS SCALE).
// Returns an error if the `zfs` binary is unavailable or the mountpoint is not
// found, so callers can fall back to the statfs values.
func getZFSUsage(mountpoint string) (totalBytes, usedBytes uint64, err error) {
	cmd := exec.Command("zfs", "list", "-H", "-p", "-o", "used,available,mountpoint")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("zfs list: %w", err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != mountpoint {
			continue
		}
		used, err1 := strconv.ParseUint(fields[0], 10, 64)
		avail, err2 := strconv.ParseUint(fields[1], 10, 64)
		if err1 != nil || err2 != nil {
			return 0, 0, fmt.Errorf("parsing zfs list output for %s", mountpoint)
		}
		return used + avail, used, nil
	}

	return 0, 0, fmt.Errorf("no ZFS dataset found for mountpoint %s", mountpoint)
}

func inferCPUTempSensor(sensors []sensors.TemperatureStat) *sensors.TemperatureStat {
	for i := range sensors {
		switch sensors[i].SensorKey {
		case
			"coretemp_package_id_0", // intel / linux
			"coretemp",              // intel / linux
			"k10temp",               // amd / linux
			"zenpower",              // amd / linux
			"cpu_thermal":           // raspberry pi / linux
			return &sensors[i]
		}
	}

	return nil
}
