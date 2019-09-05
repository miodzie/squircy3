package irc // import "code.dopame.me/veonik/squircy3/irc"

import (
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"code.dopame.me/veonik/squircy3/event"

	"github.com/pkg/errors"
	"github.com/thoj/go-ircevent"
)

type Config struct {
	Nick     string `toml:"nick"`
	Username string `toml:"user"`

	Network     string `toml:"network"`
	TLS         bool   `toml:"tls"`
	AutoConnect bool   `toml:"auto"`

	SASL         bool   `toml:"sasl"`
	SASLUsername string `toml:"sasl_username"`
	SASLPassword string `toml:"sasl_password"`
}

type Manager struct {
	config *Config
	events *event.Dispatcher
	conn   *Connection

	mu sync.RWMutex
}

type Connection struct {
	*irc.Connection

	current  Config
	quitting chan struct{}
	done     chan struct{}
}

func (conn *Connection) Connect() error {
	conn.Connection.Lock()
	defer conn.Connection.Unlock()
	return conn.Connection.Connect(conn.current.Network)
}

func (conn *Connection) Quit() error {
	select {
	case <-conn.done:
		// already done, nothing to do

	case <-conn.quitting:
		// already quitting, nothing to do

	default:
		fmt.Println("quitting")
		conn.Connection.Quit()
		close(conn.quitting)
	}
	// block until done
	select {
	case <-conn.done:
		break

	case <-time.After(1 * time.Second):
		conn.Connection.Disconnect()
		return errors.Errorf("timed out waiting for quit")
	}
	return nil
}

func (conn *Connection) controlLoop() {
	errC := conn.ErrorChan()
	for {
		select {
		case err, ok := <-errC:
			fmt.Println("read from errC in controlLoop")
			if !ok {
				// channel was closed
				fmt.Println("conn errs already closed")
				continue
			}
			fmt.Println("got err from conn:", err)
			conn.Lock()
			co := conn.Connected()
			conn.Unlock()
			if !co {
				// all done!
				close(conn.done)
				return
			}
		}
	}
}

func NewManager(c *Config, ev *event.Dispatcher) *Manager {
	return &Manager{config: c, events: ev}
}

func (m *Manager) Do(fn func(*Connection) error) error {
	m.mu.RLock()
	conn := m.conn
	m.mu.RUnlock()
	if conn == nil {
		return errors.New("not connected")
	}
	conn.Lock()
	defer conn.Unlock()
	return fn(conn)
}

func (m *Manager) Connection() (*Connection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.conn == nil {
		return nil, errors.New("not connected")
	}
	return m.conn, nil
}

func newConnection(c Config) *Connection {
	conn := &Connection{
		current:  c,
		quitting: make(chan struct{}),
		done:     make(chan struct{}),
	}
	conn.Connection = irc.IRC(c.Nick, c.Username)
	if c.TLS {
		conn.UseTLS = true
		conn.TLSConfig = &tls.Config{}
	}
	if c.SASL {
		conn.UseSASL = true
		conn.SASLLogin = c.SASLUsername
		conn.SASLPassword = c.SASLPassword
	}
	conn.QuitMessage = "farewell"
	return conn
}

func (m *Manager) Connect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil {
		return errors.New("already connected")
	}
	m.conn = newConnection(*m.config)
	m.conn.AddCallback("*", func(ev *irc.Event) {
		m.events.Emit("irc."+ev.Code, map[string]interface{}{
			"User":    ev.User,
			"Host":    ev.Host,
			"Source":  ev.Source,
			"Code":    ev.Code,
			"Message": ev.Message(),
			"Nick":    ev.Nick,
			"Target":  ev.Arguments[0],
			"Raw":     ev.Raw,
			"Args":    append([]string{}, ev.Arguments...),
		})
	})
	err := m.conn.Connect()
	if err == nil {
		go m.conn.controlLoop()
		go func() {
			m.events.Emit("irc.CONNECT", nil)
			<-m.conn.done
			m.events.Emit("irc.DISCONNECT", nil)
			m.mu.Lock()
			defer m.mu.Unlock()
			m.conn = nil
		}()
	}
	return err
}

func (m *Manager) Disconnect() error {
	m.mu.RLock()
	conn := m.conn
	m.mu.RUnlock()
	if conn == nil {
		return errors.New("not connected")
	}
	return conn.Quit()
}