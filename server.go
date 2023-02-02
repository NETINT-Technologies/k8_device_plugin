package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1beta1"
)

type NiDeviceInfo struct {
	Namespace    int    `json:"NameSpace"`
	DevicePath   string `json:"DevicePath"`
	Firmware     string `json:"Firmware"`
	Index        int    `json:"Index"`
	ModelNumber  string `json:"ModelNumber"`
	ProductName  string `json:"ProductName"`
	SerialNumber string `json:"SerialNumber"`
	UsedBytes    int    `json:"UsedBytes"`
	MaximumLBA   int    `json:"MaximumLBA"`
	PhysicalSize int    `json:"PhysicalSize"`
	SectorSize   int    `json:"SectorSize"`
}

//type NiDeviceIdentifyInfo struct {
//	Vid string `json:"vid"`
//	Ssvid string `json:"ssvid"`
//	Sn string `json:"sn"`
//	Mn string `json:"mn"`
//	Fr string `json:"fr"`
//	Rab string `json:"rab"`
//}

type ServerType int32

const (
	LOGAN  ServerType = 0
	QUADRA ServerType = 1
)

type NiDeviceInfos struct {
	Devices []NiDeviceInfo `json:"Devices"`
}

type NiDevice struct {
	pluginapi.Device
	Path string
}

const (
	resourceName string = "netint.ca/ASIC"
	NetintSocket string = "Netint.sock"
	resourceNameQuadra string = "netint.ca/Quadra"
	NetintSocketQuadra string = "Netint.sockQuadra"
	// KubeletSocket kubelet 监听 unix 的名称
	KubeletSocket string = "kubelet.sock"
	// DevicePluginPath 默认位置
	DevicePluginPath string = "/var/lib/kubelet/device-plugins/"

	envDisableHealthChecks        = "NI_DISABLE_HEALTHCHECKS" //"all" means disable all check
	showMonitorLog                = "showMonitorLog"                       //Y or N
	virtualNum             string = "virtualNumber"
	virtualNumQuadra             string = "virtualNumQuadra"
	defaultVirtNum         int    = 1
)

// NetintServer 是一个 device plugin server
type NetintServer struct {
	srv           *grpc.Server
	cachedDevices []*NiDevice
	ctx           context.Context
	cancel        context.CancelFunc
	virtualNum    int

	restartFlag bool // 本次是否是重启
	unHealth    chan *NiDevice
	health      chan *NiDevice
	stop        chan interface{}
	update      chan []*NiDevice

	resourceName string
	netintSocket string
	regPattern   string
}

// NewNetintServer 实例化 NetintServer
func NewNetintServer(serverType ServerType) *NetintServer {
	ctx, cancel := context.WithCancel(context.Background())
	if serverType == LOGAN{
		vNum, err := strconv.Atoi(os.Getenv(virtualNum))
		if err != nil {
			vNum = defaultVirtNum
		}
		return &NetintServer{
			ctx:           ctx,
			cancel:        cancel,
			srv:           nil,
			cachedDevices: nil,
			unHealth:      nil,
			health:        nil,
			stop:          nil,
			update:        nil,
			resourceName:  resourceName,
			netintSocket: NetintSocket,
			regPattern: "T4\\d\\d-.*",
			virtualNum: vNum,
		}
	} else {
		vNum, err := strconv.Atoi(os.Getenv(virtualNumQuadra))
		if err != nil {
			vNum = defaultVirtNum
		}
		return &NetintServer{
			ctx:           ctx,
			cancel:        cancel,
			srv:           nil,
			cachedDevices: nil,
			unHealth:      nil,
			health:        nil,
			stop:          nil,
			update:        nil,
			resourceName:  resourceNameQuadra,
			netintSocket: NetintSocketQuadra,
			regPattern: "Quadra.*",
			virtualNum: vNum,
		}
	}

}

// Run 运行服务
func (s *NetintServer) Run() error {
	// 发现本地设备
	err := s.initialize()
	if err != nil {
		//No card bus still can run the device plugin
		log.Fatalf("list device error: %v", err)
	}

	//err = s.Serve()
	go s.Serve()
	if err != nil {
		//Can't start grp server, need to restart
		log.Printf("Could not start device plugin for '%s': %s", s.resourceName, err)
		s.cleanup()
		return err
	}

	err = s.RegisterToKubelet()
	if err != nil {
		log.Printf("Could not register device plugin: %s", err)
		s.Stop()
		return err
	}

	go checkHealth(s)
	return nil
}

// Stop stops the gRPC server.
func (s *NetintServer) Stop() error {
	if s == nil || s.srv == nil {
		return nil
	}
	log.Printf("Stopping to serve '%s' on %s", s.resourceName, DevicePluginPath+s.netintSocket)
	s.srv.Stop()
	if err := os.Remove(DevicePluginPath + s.netintSocket); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.cleanup()
	return nil
}

func (s *NetintServer) cleanup() {
	close(s.stop)
	close(s.update)
	close(s.health)
	close(s.unHealth)
	s.cachedDevices = nil
	s.health = nil
	s.unHealth = nil
	s.stop = nil
	s.update = nil
}

// RegisterToKubelet 向kubelet注册device plugin
func (s *NetintServer) RegisterToKubelet() error {
	socketFile := filepath.Join(DevicePluginPath + KubeletSocket)

	conn, err := s.dial(socketFile, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	req := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(DevicePluginPath + s.netintSocket),
		ResourceName: s.resourceName,
	}
	log.Warnf("Register to kubelet with endpoint %s", req.Endpoint)
	_, err = client.Register(context.Background(), req)
	if err != nil {
		return err
	}

	return nil
}

// GetDevicePluginOptions returns options to be communicated with Device
// Manager
func (s *NetintServer) GetDevicePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	log.Infoln("GetDevicePluginOptions called")
	return &pluginapi.DevicePluginOptions{PreStartRequired: true}, nil
}

func (s *NetintServer) apiDevices() []*pluginapi.Device {
	var pdevs []*pluginapi.Device
	for _, d := range s.cachedDevices {
		pdevs = append(pdevs, &d.Device)
		log.Infoln("apiDevices dev = '%s', '%s'", d.Path, d.ID)
	}
	return pdevs
}

func (s *NetintServer) apiDeviceSpecs(filter []string) []*pluginapi.DeviceSpec {
	var specs []*pluginapi.DeviceSpec

	for _, d := range s.cachedDevices {
		for _, id := range filter {
			if d.ID == id {
				spec := &pluginapi.DeviceSpec{
					ContainerPath: d.Path,
					HostPath:      d.Path,
					Permissions:   "rw",
				}
				specs = append(specs, spec)
				r, _ := regexp.Compile("(.*)n\\d+")
				charDev := r.FindAllStringSubmatch(d.Path, -1)[0][1]
				log.Printf("char device is %s", charDev)
				spec = &pluginapi.DeviceSpec{
					ContainerPath: charDev,
					HostPath:      charDev,
					Permissions:   "rw",
				}
				specs = append(specs, spec)
			}
		}
	}

	return specs
}

func (s *NetintServer) deviceExists(id string) bool {
	for _, d := range s.cachedDevices {
		if d.ID == id {
			return true
		}
	}
	return false
}

// ListAndWatch returns a stream of List of Devices
// Whenever a Device state change or a Device disappears, ListAndWatch
// returns the new list
func (s *NetintServer) ListAndWatch(e *pluginapi.Empty, srv pluginapi.DevicePlugin_ListAndWatchServer) error {
	log.Warnln("ListAndWatch called")
	srv.Send(&pluginapi.ListAndWatchResponse{Devices: s.apiDevices()})

	for {
		select {
		case <-s.stop:
			return nil
		case dev := <-s.update:
			s.cachedDevices = dev
			log.Warnln("device number = %d", len(s.cachedDevices))
			srv.Send(&pluginapi.ListAndWatchResponse{Devices: s.apiDevices()})
		case d := <-s.unHealth:
			d.Health = pluginapi.Unhealthy
			log.Infoln("'%s' device marked unhealthy: %s", s.resourceName, d.Path)
			srv.Send(&pluginapi.ListAndWatchResponse{Devices: s.apiDevices()})
		case d := <-s.health:
			d.Health = pluginapi.Healthy
			log.Infoln("'%s' device marked healthy: %s", s.resourceName, d.Path)
			srv.Send(&pluginapi.ListAndWatchResponse{Devices: s.apiDevices()})
		}
	}
}

// Allocate is called during container creation so that the Device
// Plugin can run device specific operations and instruct Kubelet
// of the steps to make the Device available in the container
func (s *NetintServer) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	log.Warnln("Allocate called")
	responses := pluginapi.AllocateResponse{}
	for _, req := range reqs.ContainerRequests {
		for _, id := range req.DevicesIDs {
			if !s.deviceExists(id) {
				return nil, fmt.Errorf("invalid allocation request for '%s': unknown device: %s", s.resourceName, id)
			}
		}

		response := pluginapi.ContainerAllocateResponse{
			Envs: map[string]string{
				"NETINT_VISIBLE_DEVICE": strings.Join(req.DevicesIDs, ","),
			},
		}
		log.Infof("device id = %s", strings.Join(req.DevicesIDs, ","))
		response.Devices = s.apiDeviceSpecs(req.DevicesIDs)

		responses.ContainerResponses = append(responses.ContainerResponses, &response)
	}

	return &responses, nil
}

// PreStartContainer is called, if indicated by Device Plugin during registeration phase,
// before each container start. Device plugin can run device specific operations
// such as reseting the device before making devices available to the container
func (s *NetintServer) PreStartContainer(ctx context.Context, req *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	log.Infoln("PreStartContainer called")
	return &pluginapi.PreStartContainerResponse{}, nil
}

func identifyDevice(devicePath string) bool {
	cmdString := "nvme id-ctrl " + devicePath + " -o json"
	c := exec.Command("/bin/sh", "-c", cmdString)
	stdout, err := c.StdoutPipe()
	if err != nil {
		log.Fatal(err)
		return false
	}

	defer c.Wait()
	defer stdout.Close()

	if err := c.Start(); err != nil {
		log.Fatal(err)
		return false
	}

	if opBytes, err := ioutil.ReadAll(stdout); err != nil {
		log.Fatal(err)
		return false
	} else {
		compileRegex := regexp.MustCompile(".*vid.*(\\d{4}),")
		matchArr := compileRegex.FindStringSubmatch(string(opBytes))
		//log.Warnf("identify device %s",string(opBytes));
		if len(matchArr) == 0 {
			log.Warnf("NETINT devices '%s' json parse fail", devicePath)
			log.Warnf("NETINT devices '%s' is LOSS", devicePath)
			return false
		} else {
			v := matchArr[1]
			log.Infoln("find vid = %q",matchArr)
			vid,err := strconv.Atoi(v)
			if vid == 7554 {
				log.Infoln("Identify NETINT devices '%s' is OK", devicePath)
				return true
			} else {
				log.Warnf("NETINT devices '%s' vid wrong = '%G' err='%t'", devicePath, vid, err)
				return false
			}
		}
		//err = json.Unmarshal([]byte(opBytes), &identifyInfo)
		//if err == nil {
		//	for k, v := range identifyInfo.(map[string]interface{}) {
		//		if k == "vid" {
		//			vid, err := v.(float64)
		//			if err == true && math.Dim(vid, 7554) < 0.00001 {
		//				log.Infof("Identify NETINT devices '%s' is OK", devicePath)
		//				return true
		//			} else {
		//				log.Warnf("NETINT devices '%s' vid wrong = '%G' err='%t'", devicePath, vid, err)
		//				return false
		//			}
		//		}
		//	}
		//	log.Warnf("NETINT devices '%s' json parse fail", devicePath)
		//	return false
		//} else {
		//	log.Warnf("NETINT devices '%s' json parse fail", devicePath)
		//	log.Warnf("NETINT devices '%s' is LOSS", devicePath)
		//	err = nil
		//	return false
		//}
	}
}

func getDeviceInfo() (*NiDeviceInfos, error) {
	infos := NiDeviceInfos{}
	c := exec.Command("/bin/sh", "-c", "nvme list -o json")
	stdout, err := c.StdoutPipe()
	if err != nil {
		log.Fatal(err)
		return &infos, err
	}

	defer c.Wait()
	defer stdout.Close()

	if err := c.Start(); err != nil {
		log.Fatal(err)
		return &infos, err
	}

	if opBytes, err := ioutil.ReadAll(stdout); err != nil {
		log.Fatal(err)
		return &infos, err
	} else {
		if os.Getenv(showMonitorLog) == "Y" {
			log.Println(string(opBytes))
		}
		err = json.Unmarshal([]byte(opBytes), &infos)
		if err == nil {
			log.Infof("Find '%d' NETINT devices", len(infos.Devices))
		} else {
			log.Println("Can't find device, Please run 'sudo nvme list -o json' to check if the output is right")
			err = nil
			return &infos, err
		}
	}
	return &infos, err
}

// initialize 从节点上发现设备
func (s *NetintServer) initialize() error {
	niInfos, err := getDeviceInfo()
	if err != nil {
		return err
	}
	//s.virtualNum, err = strconv.Atoi(os.Getenv(virtualNum))
	//if err != nil {
	//	s.virtualNum = defaultVirtNum
	//}
	log.Warnf("virtualNum device %d", s.virtualNum)
	s.cachedDevices = nil
	reg, _ := regexp.Compile(s.regPattern)
	//reg, _ := regexp.Compile("T4\\d\\d-.*")
	//niReg, _ := regexp.Compile(".*ninvme.*")
	for _, info := range niInfos.Devices {
		if reg.MatchString(info.ModelNumber) {
			//if niReg.MatchString(info.DevicePath) {
			for i := 0; i < s.virtualNum; i++ {
				//sum := md5.Sum([]byte(info.SerialNumber))
				niDevice := NiDevice{}
				//niDevice.ID = string(sum[:])
				niDevice.Health = pluginapi.Healthy
				niDevice.Path = info.DevicePath
				niDevice.ID = fmt.Sprintf("%s-%d", info.SerialNumber, i)
				s.cachedDevices = append(s.cachedDevices, &niDevice)
			}
			log.Warnf("find device %s", info.DevicePath)
			//}
		}
	}
	s.srv = grpc.NewServer(grpc.EmptyServerOption{})
	s.health = make(chan *NiDevice, len(s.cachedDevices))
	s.unHealth = make(chan *NiDevice, len(s.cachedDevices))
	s.stop = make(chan interface{})
	s.update = make(chan []*NiDevice)

	return nil
}

func checkHealth(s *NetintServer) {
	var devices []*NiDevice = nil
	var needUpdate = false
	disableHealthChecks := strings.ToLower(os.Getenv(envDisableHealthChecks))
	if disableHealthChecks == "all" {
		return
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			niInfos, err := getDeviceInfo()
			if err != nil {
				return
			}
			reg, _ := regexp.Compile(s.regPattern)
			devices = nil
			//niReg, _ := regexp.Compile(".*ninvme.*")
			for _, info := range niInfos.Devices {
				if reg.MatchString(info.ModelNumber) {
					//if niReg.MatchString(info.DevicePath) {
					for i := 0; i < s.virtualNum; i++ {
						//	sum := md5.Sum([]byte(info.SerialNumber))
						niDevice := NiDevice{}
						//niDevice.ID = string(sum[:])
						niDevice.Health = pluginapi.Healthy
						niDevice.Path = info.DevicePath
						niDevice.ID = fmt.Sprintf("%s-%d", info.SerialNumber, i)
						devices = append(devices, &niDevice)
						if !needUpdate {
							needUpdate = true
							for _, cachedDev := range s.cachedDevices {
								if cachedDev.ID == niDevice.ID {
									needUpdate = false
								}
							}
						}
					}
					if needUpdate {
						log.Warnln("check health find device '%s'", info.DevicePath)
					}
				}
			}
			if len(s.cachedDevices) != len(devices)*s.virtualNum {
				needUpdate = true
				//devices = nil
				//log.Warningf("hot plug in/out card devices=%d,niInfos=%d", len(s.cachedDevices), len(niInfos.Devices))
				////reg, _ := regexp.Compile("T4\\d\\d-.*")
				//reg, _ := regexp.Compile(s.regPattern)
				////niReg, _ := regexp.Compile(".*ninvme.*")
				//for _, info := range niInfos.Devices {
				//	if reg.MatchString(info.ModelNumber) {
				//		//if niReg.MatchString(info.DevicePath) {
				//		for i := 0; i < s.virtualNum; i++ {
				//			//	sum := md5.Sum([]byte(info.SerialNumber))
				//			niDevice := NiDevice{}
				//			//niDevice.ID = string(sum[:])
				//			niDevice.Health = pluginapi.Healthy
				//			niDevice.Path = info.DevicePath
				//			niDevice.ID = fmt.Sprintf("%s-%d", info.SerialNumber, i)
				//
				//			devices = append(devices, &niDevice)
				//		}
				//		//devices = append(devices, &niDevice)
				//		log.Warnln("check health find device '%s'", info.DevicePath)
				//		//}
				//	}
				//}
			} else {
				for _, d := range s.cachedDevices {
					isHealthy := identifyDevice(d.Path)
					if isHealthy != (d.Health == pluginapi.Healthy) {
						if isHealthy == false {
							s.unHealth <- d
						} else {
							s.health <- d
						}
					}
					//if isHealthy == false {
					//	d.Health = pluginapi.Unhealthy
					//} else {
					//	d.Health = pluginapi.Healthy
					//}
				}
			}
			if needUpdate {
				s.update <- devices
				needUpdate = false
			}
		}
	}
}

// Serve starts the gRPC server of the device plugin.
func (s *NetintServer) Serve() error {
	pluginapi.RegisterDevicePluginServer(s.srv, s)
	err := syscall.Unlink(DevicePluginPath + s.netintSocket)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	sock, err := net.Listen("unix", DevicePluginPath+s.netintSocket)
	if err != nil {
		return err
	}

	go func() {
		lastCrashTime := time.Now()
		restartCount := 0
		for {
			log.Printf("Starting GRPC server for '%s'", s.resourceName)
			err := s.srv.Serve(sock)
			if err == nil {
				log.Printf("Starting GRPC server '%s' SUCCESS!!!", s.resourceName)
				break
			}
			log.Printf("GRPC server for '%s' crashed with error: %v", s.resourceName, err)

			// restart if it has not been too often
			// i.e. if server has crashed more than 5 times and it didn't last more than one hour each time
			if restartCount > 5 {
				// quit
				log.Fatal("GRPC server for '%s' has repeatedly crashed recently. Quitting", s.resourceName)
			}
			timeSinceLastCrash := time.Since(lastCrashTime).Seconds()
			lastCrashTime = time.Now()
			if timeSinceLastCrash > 3600 {
				// it has been one hour since the last crash.. reset the count
				// to reflect on the frequency
				restartCount = 1
			} else {
				restartCount += 1
			}
		}
	}()

	// Wait for server to start by launching a blocking connexion
	conn, err := s.dial(s.netintSocket, 5*time.Second)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

func (s *NetintServer) dial(unixSocketPath string, timeout time.Duration) (*grpc.ClientConn, error) {
	c, err := grpc.Dial(unixSocketPath, grpc.WithInsecure(), grpc.WithBlock(),
		grpc.WithTimeout(timeout),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)

	if err != nil {
		return nil, err
	}

	return c, nil
}
