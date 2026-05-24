package watcher

import (
	"bufio"
	"context"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/moehoshio/nginx-request-attribution/internal/parser"
	"github.com/moehoshio/nginx-request-attribution/internal/storage"
)

// SyslogReceiver listens for syslog messages containing nginx access log lines.
type SyslogReceiver struct {
	store    *storage.Store
	addr     string
	proto    string
	keywords []string
}

// NewSyslogReceiver creates a new syslog receiver.
// proto can be "udp", "tcp", or "both".
func NewSyslogReceiver(store *storage.Store, addr string, proto string, keywords []string) *SyslogReceiver {
	if proto == "" {
		proto = "udp"
	}
	return &SyslogReceiver{
		store:    store,
		addr:     addr,
		proto:    proto,
		keywords: keywords,
	}
}

// Listen starts listening for syslog messages.
func (sr *SyslogReceiver) Listen(ctx context.Context) error {
	var wg sync.WaitGroup

	switch sr.proto {
	case "udp":
		return sr.listenUDP(ctx)
	case "tcp":
		return sr.listenTCP(ctx)
	case "both":
		errCh := make(chan error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := sr.listenUDP(ctx); err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			if err := sr.listenTCP(ctx); err != nil {
				errCh <- err
			}
		}()
		wg.Wait()
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	default:
		return sr.listenUDP(ctx)
	}
}

func (sr *SyslogReceiver) listenUDP(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", sr.addr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}

	log.Printf("Syslog UDP receiver listening on %s", sr.addr)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 65535)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}

		message := string(buf[:n])
		sr.processMessage(message)
	}
}

func (sr *SyslogReceiver) listenTCP(ctx context.Context) error {
	listener, err := net.Listen("tcp", sr.addr)
	if err != nil {
		return err
	}

	log.Printf("Syslog TCP receiver listening on %s", sr.addr)

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go sr.handleTCPConn(ctx, conn)
	}
}

func (sr *SyslogReceiver) handleTCPConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		sr.processMessage(scanner.Text())
	}
}

// processMessage extracts the nginx log line from a syslog message and processes it.
// Syslog format: <priority>timestamp hostname tag: message
// We try to extract the actual nginx log line from the syslog wrapper.
func (sr *SyslogReceiver) processMessage(message string) {
	line := extractLogLine(message)
	if line == "" {
		return
	}

	entry, err := parser.ParseLine(line)
	if err != nil {
		return
	}

	if err := sr.store.Insert(entry, sr.keywords); err != nil {
		log.Printf("Syslog receiver insert error: %v", err)
	}
}

// extractLogLine strips the syslog header to get the raw nginx log line.
// Handles RFC 3164 format: <PRI>Mmm dd hh:mm:ss hostname tag[pid]: message
// Also handles plain nginx log lines sent directly.
func extractLogLine(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}

	// If message starts with '<', it has a syslog priority header
	if message[0] == '<' {
		// Find end of priority
		idx := strings.IndexByte(message, '>')
		if idx < 0 {
			return message
		}
		message = message[idx+1:]

		// RFC 3164: after priority comes "Mmm dd hh:mm:ss hostname tag[pid]: msg"
		// Look for the colon+space that separates header from message
		colonIdx := strings.Index(message, ": ")
		if colonIdx >= 0 {
			message = message[colonIdx+2:]
		}
	}

	return strings.TrimSpace(message)
}
