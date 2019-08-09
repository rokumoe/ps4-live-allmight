package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/vizee/dnsproxy"
	"github.com/vizee/rtmpproxy"
)

var wg sync.WaitGroup

func startRTMPProxy(bind string, pubUrl string) {
	u, err := url.Parse(pubUrl)
	if err != nil {
		log.Fatalf("[rtmp] url parse: %v", err)
	}
	host, port, _ := net.SplitHostPort(u.Host)
	if port == "" {
		host = u.Host
		port = "1935"
	}
	appName := strings.Trim(u.Path, "/")
	s := rtmpproxy.NewServer(net.JoinHostPort(host, port), appName, fmt.Sprintf("rtmp://%s/%s", u.Host, appName), "?"+u.RawQuery)
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, "1935"))
	if err != nil {
		log.Fatalf("[rtmp] listen: %v", err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("[rtmp] accept: %v", err)
				time.Sleep(time.Second)
				continue
			}
			go func(conn net.Conn) {
				err := s.Serve(conn)
				if err != nil {
					log.Printf("[rtmp] serve: %v", err)
				}
			}(conn)
		}
	}()
}

func startForwardProxy(bind string, dst string) {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, "8080"))
	if err != nil {
		log.Fatalf("[forward] listen: %v", err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("[forward] accept: %v", err)
				time.Sleep(time.Second)
				continue
			}
			go func(sc net.Conn) {
				defer sc.Close()
				dc, err := net.Dial("tcp", dst)
				if err != nil {
					log.Printf("[forward] dial: %v", err)
					return
				}
				defer dc.Close()
				go func() {
					io.Copy(dc, sc)
					sc.Close()
					dc.Close()
				}()
				io.Copy(sc, dc)
			}(conn)
		}
	}()
}

func startDNSProxy(bind string) {
	bindIP := net.ParseIP(bind).To4()
	if len(bindIP) != net.IPv4len {
		log.Fatalf("IPv4 only: %s", bind)
	}
	ips := []net.IP{bindIP}
	hijackList := []string{
		`^live-.*\.twitch\.tv$`,
	}
	pc := &dnsproxy.ProxyConfig{}
	for _, h := range hijackList {
		re, err := regexp.Compile(h)
		if err != nil {
			log.Fatalf("[dnsproxy] pattern %s: %v", h, err)
		}
		pc.Hosts = append(pc.Hosts, &dnsproxy.HostItem{
			Pattern: h,
			RE:      re,
			IPs:     ips,
		})
	}
	dnsproxy.SetProxyConf(pc)
	err := dnsproxy.LoadResolvConf("/etc/resolv.conf")
	if err != nil {
		log.Fatalf("[dnsproxy] load resolvconf: %v", err)
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		err := dnsproxy.ServeTCP("tcp", net.JoinHostPort(bind, "53"))
		if err != nil {
			log.Fatalf("[dnsproxy] serve tcp: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		err := dnsproxy.ServeUDP("udp", net.JoinHostPort(bind, "53"))
		if err != nil {
			log.Fatalf("[dnsproxy] serve udp: %v", err)
		}
	}()

}

func main() {
	var (
		bind      string
		hijackDNS bool
		streamUrl string
		forward   string
	)
	flag.StringVar(&bind, "b", "127.0.0.1", "local bind IP")
	flag.BoolVar(&hijackDNS, "s", false, "dns proxy on <bind>:53")
	flag.StringVar(&streamUrl, "r", "rtmp://server/app/?stream", "rtmp proxy on <bind>:1935")
	flag.StringVar(&forward, "f", "", "tcp proxy on <bind>:8080")
	flag.Parse()

	nifs, _ := net.InterfaceAddrs()
	found := false
	for _, nif := range nifs {
		if strings.HasPrefix(nif.String(), bind+"/") {
			found = true
			break
		}
	}
	if !found {
		log.Fatalf("invalid bind IP: %s", bind)
	}

	if hijackDNS {
		startDNSProxy(bind)
		log.Printf("dnsproxy started")
	}
	if streamUrl != "" {
		startRTMPProxy(bind, streamUrl)
		log.Printf("rtmpproxy started")
	}
	if forward != "" {
		startForwardProxy(bind, forward)
		log.Printf("forwardproxy started")
	}
	wg.Wait()
}
