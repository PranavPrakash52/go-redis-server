package server

import (
	"go-redis-server/config"
	"go-redis-server/core"
	"net"
	"syscall"
	"time"
)

func RunAsyncTCPServerDarwin() error {

	lastCronTime := time.Now().UnixMilli()

	var max_clients int = 20000

	var events []syscall.Kevent_t = make([]syscall.Kevent_t, max_clients)

	// Create socket
	socketFD, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(socketFD)

	err = syscall.SetNonblock(socketFD, true)
	if err != nil {
		return err
	}

	ip := net.ParseIP(config.Host)

	sock := syscall.SockaddrInet4{
		Port: config.Port,
		Addr: [4]byte{ip[0], ip[1], ip[2], ip[3]},
	}

	err = syscall.Bind(socketFD, &sock)
	if err != nil {
		return err
	}

	err = syscall.Listen(socketFD, max_clients)
	if err != nil {
		return err
	}

	// Async part

	kqueueFD, err := syscall.Kqueue()
	if err != nil {
		return err
	}
	defer syscall.Close(kqueueFD)

	var ev syscall.Kevent_t = syscall.Kevent_t{
		Ident:  uint64(socketFD),
		Filter: syscall.EVFILT_READ,
		Flags:  syscall.EV_ADD | syscall.EV_ENABLE,
	}

	_, err = syscall.Kevent(kqueueFD, []syscall.Kevent_t{ev}, nil, nil)
	if err != nil {
		return err
	}

	for {
		if time.Now().After(time.UnixMilli(lastCronTime + int64(config.CronInterval))) {
			lastCronTime = time.Now().UnixMilli()
			core.ClearExpired()
		}

		nevents, err := syscall.Kevent(kqueueFD, nil, events[:], nil)
		if err != nil {
			return err
		}

		for i := 0; i < int(nevents); i++ {
			if events[i].Ident == uint64(socketFD) {
				clientFD, _, err := syscall.Accept(socketFD)
				if err != nil {
					continue
				}
				syscall.SetNonblock(clientFD, true)
				var ev syscall.Kevent_t = syscall.Kevent_t{
					Ident:  uint64(clientFD),
					Filter: syscall.EVFILT_READ,
					Flags:  syscall.EV_ADD | syscall.EV_ENABLE,
				}
				_, err = syscall.Kevent(kqueueFD, []syscall.Kevent_t{ev}, nil, nil)
				if err != nil {
					return err
				}
			} else {
				comm := core.FDComm{Fd: int(events[i].Ident)}
				cmds, err := readCommand(comm)
				if err != nil {
					syscall.Close(int(events[i].Ident))
					continue
				}
				core.EvalAndRespond(cmds, comm)
			}
		}

	}
}
