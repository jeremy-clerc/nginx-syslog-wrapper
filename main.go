package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"syscall"
	"time"
)

var nginxSyslogRegexp = regexp.MustCompile("(?P<pri><[0-9]+>)(?P<ts>[A-Za-z]{3} [ 0-9][0-9] [0-9]{2}:[0-9]{2}:[0-9]{2}) (?P<hostname>[^[:blank:]]+) (?P<app>[^[:blank:]:]+): (?P<msg>.*)")

func handle(ctx context.Context, listner *net.UDPConn, sender net.Conn) {
	buf := make([]byte, 1024)
	read := make(chan int)

	for {
		go func() {
			n, err := listner.Read(buf)
			if err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) {
					n = -1
				} else {
					log.Fatal(err)
				}
			}
			read <- n
		}()
		n := 0
		select {
		case <-ctx.Done():
			return
		case n = <-read:
			if n == -1 {
				continue
			}
		}

		m := nginxSyslogRegexp.FindSubmatch(buf[:n])
		if m == nil {
			log.Printf("Invalid log line: %s", buf[:n])
			continue
		}

		t, err := time.Parse(time.Stamp, string(m[nginxSyslogRegexp.SubexpIndex("ts")]))
		if err != nil {
			log.Printf("Invalidate date: %s", buf[:n])
			continue
		}

		var wbuf bytes.Buffer
		wbuf.Write(m[nginxSyslogRegexp.SubexpIndex("pri")])
		wbuf.Write([]byte("1 "))
		wbuf.WriteString(t.AddDate(time.Now().Year(), 0, 0).Format(time.RFC3339))
		wbuf.Write([]byte(" "))
		wbuf.Write(m[nginxSyslogRegexp.SubexpIndex("hostname")])
		wbuf.Write([]byte(" "))
		wbuf.Write(m[nginxSyslogRegexp.SubexpIndex("app")])
		wbuf.Write([]byte(" - - "))
		wbuf.Write(m[nginxSyslogRegexp.SubexpIndex("msg")])
		wbuf.Write([]byte("\n"))

		if _, err = sender.Write(wbuf.Bytes()); err != nil {
			log.Fatal(err)
		}
	}
}

func main() {
	var (
		listen = flag.String("listen", "127.0.0.1:5140", "Socket to listen and receive nginx logs")
		sendTo = flag.String("send-to", "127.0.0.1:514", "Syslog socket to write to")
	)
	flag.Parse()

	addr, err := net.ResolveUDPAddr("udp", *listen)
	if err != nil {
		log.Fatal(err)
	}

	listner, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatal(err)
	}
	defer listner.Close()

	sender, err := net.Dial("udp", *sendTo)
	if err != nil {
		log.Fatal(err)
	}
	defer sender.Close()

	ctx, _ := signal.NotifyContext(context.Background())

	if flag.NArg() > 0 {
		go handle(ctx, listner, sender)

		var cmdArgs []string
		if flag.NArg() > 1 {
			cmdArgs = flag.Args()[1:]
		}
		cmd := exec.CommandContext(ctx, flag.Args()[0], cmdArgs...)
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		cmd.Cancel = func() error {
			return cmd.Process.Signal(syscall.SIGQUIT)
		}
		if err := cmd.Run(); err != nil {
			log.Fatal(err)
		}
	} else {
		handle(ctx, listner, sender)
	}
}
