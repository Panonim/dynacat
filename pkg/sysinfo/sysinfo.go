package sysinfo

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
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

type cacheableHostInfo struct {
	available bool
	hostname  string
	platform  string
	bootTime  timestampJSON
}

var cachedHostInfo cacheableHostInfo

// Paths the host's os-release can be bind mounted to when Dynacat runs in a container,
// where /etc/os-release describes the image instead of the machine.
var hostOSReleasePaths = []string{"/host/etc/os-release", "/host/os-release"}

func hostOSRelease() string {
	paths := hostOSReleasePaths
	if hostEtc := os.Getenv("HOST_ETC"); hostEtc != "" {
		paths = append([]string{filepath.Join(hostEtc, "os-release")}, paths...)
	}

	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		for _, line := range strings.Split(string(contents), "\n") {
			if id, ok := strings.CutPrefix(strings.TrimSpace(line), "ID="); ok {
				if id = strings.Trim(id, `"'`); id != "" {
					return id
				}
			}
		}
	}

	return ""
}

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

	if platform := hostOSRelease(); platform != "" {
		info.platform = platform
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

	if runtime.GOOS != "windows" && runtime.GOOS != "openbsd" && runtime.GOOS != "netbsd" && runtime.GOOS != "freebsd" {
		sensorReadings, err := sensors.SensorsTemperatures()
		_, errIsWarning := err.(*sensors.Warnings)
		if err == nil || errIsWarning {
			if req.CPUTempSensor != "" {
				if sensor := findTempSensor(sensorReadings, req.CPUTempSensor); sensor != nil {
					info.CPU.TemperatureIsAvailable = true
					info.CPU.TemperatureC = uint8(sensor.Temperature)
				} else {
					addErr(fmt.Errorf(
						"CPU temperature sensor %s not found, available sensors: %s",
						req.CPUTempSensor, strings.Join(sensorKeys(sensorReadings), ", "),
					))
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

		if len(addedMountpoints) == 0 {
			addMountpointInfo("/", req.Mountpoints["/"])
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

// Chips that expose a CPU temperature, in the order they should be preferred. Readings are
// keyed as "<chip>" or "<chip>_<label>", so these are matched as chip prefixes.
var cpuTempChips = []string{
	"coretemp",    // intel / linux
	"k10temp",     // amd / linux
	"zenpower",    // amd / linux
	"cpu_thermal", // raspberry pi / linux
	"acpitz",      // generic fallback
}

// Labels preferred when a chip reports several temperatures, e.g. k10temp exposes Tctl
// alongside a per-die Tccd1 and Tccd2.
var cpuTempLabels = []string{"package_id_0", "tctl", "tdie", "cpu"}

func inferCPUTempSensor(readings []sensors.TemperatureStat) *sensors.TemperatureStat {
	for _, chip := range cpuTempChips {
		if sensor := sensorForChip(readings, chip, ""); sensor != nil {
			return sensor
		}
	}

	return nil
}

func sensorKeys(readings []sensors.TemperatureStat) []string {
	keys := make([]string, len(readings))
	for i := range readings {
		keys[i] = readings[i].SensorKey
	}

	return keys
}

func normalizeSensorName(name string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}

	return strings.TrimSuffix(b.String(), "_")
}

func chipCandidates(chip string) []string {
	parts := strings.Split(chip, "_")

	candidates := make([]string, 0, len(parts))
	for i := len(parts); i > 0; i-- {
		candidates = append(candidates, strings.Join(parts[:i], "_"))
	}

	return candidates
}

func sensorForChip(readings []sensors.TemperatureStat, chip, label string) *sensors.TemperatureStat {
	var matches []int

	for i := range readings {
		key := normalizeSensorName(readings[i].SensorKey)
		if readings[i].Temperature <= 0 || (key != chip && !strings.HasPrefix(key, chip+"_")) {
			continue
		}

		if label != "" {
			if key == chip+"_"+label {
				return &readings[i]
			}
			continue
		}

		matches = append(matches, i)
	}

	if len(matches) == 0 {
		return nil
	}

	for _, preferred := range append([]string{""}, cpuTempLabels...) {
		want := chip
		if preferred != "" {
			want = chip + "_" + preferred
		}

		for _, i := range matches {
			if normalizeSensorName(readings[i].SensorKey) == want {
				return &readings[i]
			}
		}
	}

	return &readings[matches[0]]
}

func findTempSensor(readings []sensors.TemperatureStat, want string) *sensors.TemperatureStat {
	for i := range readings {
		if readings[i].SensorKey == want {
			return &readings[i]
		}
	}

	chip, label, hasLabel := strings.Cut(want, "/")
	normChip, normLabel := normalizeSensorName(chip), normalizeSensorName(label)

	if normChip != "" {
		for _, candidate := range chipCandidates(normChip) {
			if sensor := sensorForChip(readings, candidate, normLabel); sensor != nil {
				return sensor
			}
		}
	}

	// A name given without a chip is a label, e.g. "Tctl" for "k10temp_tctl".
	if !hasLabel {
		normLabel = normChip
	}

	if normLabel != "" {
		for i := range readings {
			if strings.HasSuffix(normalizeSensorName(readings[i].SensorKey), "_"+normLabel) {
				return &readings[i]
			}
		}
	}

	return nil
}
