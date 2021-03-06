package socks5_server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"

	log "github.com/sirupsen/logrus"

	"github.com/neex/tcp-over-http/client/forwarder"
)

type Socks5Server struct {
	Forwarder *forwarder.Forwarder
}

func (p *Socks5Server) ListenAndServe(ctx context.Context, addr string) error {
	newCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	lc := &net.ListenConfig{}
	lsn, err := lc.Listen(newCtx, "tcp", addr)
	if err != nil {
		return err
	}

	go func() {
		<-newCtx.Done()
		_ = lsn.Close()
	}()

	log.Info("socks5 server started")

	for {
		conn, err := lsn.Accept()
		if err != nil {
			return err
		}

		go func() {
			l := log.WithField("remote_addr", conn.RemoteAddr())
			err := p.handleConn(newCtx, conn)
			if err != nil {
				l.WithError(err).Warn("socks5 client handle error")
			} else {
				l.Debug("socks5 client handling finished")
			}
		}()
	}
}

func (p *Socks5Server) handleConn(ctx context.Context, conn net.Conn) error {
	newCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-newCtx.Done()
		_ = conn.Close()
	}()

	buf := make([]byte, 1024)
	if n, err := io.ReadFull(conn, buf[:2]); n != 2 || err != nil {
		return fmt.Errorf("read short during first msg, %v", err)
	}

	if buf[0] != 5 {
		return fmt.Errorf("wrong first byte, %v", buf[0])
	}

	cntAuth := int(buf[1])
	if n, err := io.ReadFull(conn, buf[:cntAuth]); n != cntAuth || err != nil {
		return fmt.Errorf("read short during reading auth methods, %v", err)
	}

	var auth byte = 0xff

	for i := 0; i < cntAuth; i++ {
		if buf[i] == 0 {
			auth = 0
		}
	}

	buf[0] = 0x5
	buf[1] = auth

	if n, err := conn.Write(buf[:2]); n != 2 || err != nil {
		return fmt.Errorf("invalid connection attempt: write short during hello, %v", err)
	}

	if auth != 0 {
		return errors.New("auth not found")
	}

	if n, err := io.ReadFull(conn, buf[:4]); n != 4 || err != nil {
		return fmt.Errorf("read short during request, %v", err)
	}

	resp := make([]byte, 10)
	resp[0] = 5
	resp[3] = 1

	if buf[0] != 5 || buf[1] != 1 || buf[2] != 0 {
		resp[1] = 7
		_, _ = conn.Write(resp)
		return fmt.Errorf("invalid request, %v", buf[:4])
	}

	var host string
	switch buf[3] {
	case 1:
		if n, err := io.ReadFull(conn, buf[:4]); n != 4 || err != nil {
			return fmt.Errorf("read short during ipv4 read, %v", err)
		}
		host = net.IP(buf[:4]).String()

	case 3:
		if n, err := io.ReadFull(conn, buf[:1]); n != 1 || err != nil {
			return fmt.Errorf("read short during hostname len read, %v", err)
		}
		l := int(buf[0])
		if n, err := io.ReadFull(conn, buf[:l]); n != l || err != nil {
			return fmt.Errorf("read short during hostname read, %v", err)
		}
		host = string(buf[:l])

	case 4:
		if n, err := io.ReadFull(conn, buf[:16]); n != 16 || err != nil {
			return fmt.Errorf("read short during ipv6 read, %v", err)
		}
		host = net.IP(buf[:16]).String()

	default:
		resp[1] = 7
		_, _ = conn.Write(resp)
		return fmt.Errorf("invalid request, %v", buf[:4])
	}

	if n, err := io.ReadFull(conn, buf[:2]); n != 2 || err != nil {
		return fmt.Errorf("read short during port read, %v", err)
	}

	port := strconv.Itoa(int(buf[0])*256 + int(buf[1]))
	address := net.JoinHostPort(host, port)

	err := p.Forwarder.ForwardConnection(ctx, &forwarder.ForwardRequest{
		ClientConn: conn,
		Network:    "tcp",
		Address:    address,
		OnConnected: func() {
			_, _ = conn.Write(resp)
		},
	})

	if err != nil {
		resp[1] = 4
		_, _ = conn.Write(resp)
		return fmt.Errorf("host unreachable, %v", err)
	}

	return nil
}
