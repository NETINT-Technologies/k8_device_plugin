package main

import (
	"github.com/fsnotify/fsnotify"
	log "github.com/sirupsen/logrus"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1beta1"
	"os"
	"os/signal"
	"syscall"
)

func newFSWatcher(files ...string) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		err = watcher.Add(f)
		if err != nil {
			watcher.Close()
			return nil, err
		}
	}

	return watcher, nil
}

func newOSWatcher(sigs ...os.Signal) chan os.Signal {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, sigs...)

	return sigChan
}

func startServer (srv *NetintServer) (chan struct{},error) {
	pluginStartError := make(chan struct{})
	//netintSrv.Stop()

	// Start the gRPC server for netintSrv and connect it with the kubelet.
	if err := srv.Run(); err != nil {
		log.Println("Could not contact Kubelet, retrying. Did you enable the device plugin feature gate?")
		log.Printf("You can check the prerequisites ")
		close(pluginStartError)
		return pluginStartError,err
	}

	if len(srv.cachedDevices) == 0 {
		log.Println("No devices found. Waiting indefinitely.")
	}
	return pluginStartError,nil
}

func main() {
	isLogan:= false
	isQuadra:= false
	var netintSrv *NetintServer = nil
	var netintQuadraSrv *NetintServer = nil

	if os.Getenv("HasLogan") == "Y" {
		isLogan = true
	}
	if os.Getenv("HasQuadra") == "Y" {
		isQuadra = true
	}

	log.SetLevel(log.WarnLevel)
	log.Info("netint device plugin starting")
	log.Println("Starting FS watcher.")
	watcher, err := newFSWatcher(pluginapi.DevicePluginPath)
	if err != nil {
		log.Println("Failed to created FS watcher.")
		os.Exit(1)
	}
	defer watcher.Close()

	log.Println("Starting OS watcher.")
	sigs := newOSWatcher(syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	if isLogan {
		netintSrv = NewNetintServer(LOGAN)
	}
	if isQuadra {
		netintQuadraSrv = NewNetintServer(QUADRA)
	}

restartLogan:
	var pluginStartError chan struct{}
	var pluginQuadraStartError chan struct{}

	if isLogan {
		pluginStartError,err = startServer(netintSrv)
	}
	if err != nil {
		goto events
	}
restartQuadra:
	if isQuadra {
		pluginQuadraStartError,err = startServer(netintQuadraSrv)
	}
	if err != nil {
		goto events
	}
events:
	// Start an infinite loop, waiting for several indicators to either log
	// some messages, trigger a restart of the plugins, or exit the program.
	for {
		select {
		// If there was an error starting any plugins, restart them all.
		case <-pluginStartError:
			goto restartLogan
		case <-pluginQuadraStartError:
			goto restartQuadra
		// Detect a kubelet restart by watching for a newly created
		// 'pluginapi.KubeletSocket' file. When this occurs, restart this loop,
		// restarting all of the plugins in the process.
		case event := <-watcher.Events:
			if event.Name == pluginapi.KubeletSocket && event.Op&fsnotify.Create == fsnotify.Create {
				log.Printf("inotify: %s created, restarting.", pluginapi.KubeletSocket)
				goto restartLogan
			}

		// Watch for any other fs errors and log them.
		case err := <-watcher.Errors:
			log.Printf("inotify: %s", err)

		// Watch for any signals from the OS. On SIGHUP, restart this loop,
		// restarting all of the plugins in the process. On all other
		// signals, exit the loop and exit the program.
		case s := <-sigs:
			switch s {
			case syscall.SIGHUP:
				log.Info("Received SIGHUP, restarting.")
				goto restartLogan
			default:
				log.Info("Received signal \"%v\", shutting down.", s)
				if isLogan {
					netintSrv.Stop()
				}
				if isQuadra {
					netintQuadraSrv.Stop()
				}
				break events
			}
		}
	}
}
