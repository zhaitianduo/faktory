package worq

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/mperham/worq/storage"
)

type Server struct {
	Binding    string
	pwd        string
	listener   net.Listener
	processors map[string]chan *Connection
	store      *storage.Store
}

func NewServer(binding string) *Server {
	if binding == "" {
		binding = "localhost:7419"
	}
	return &Server{Binding: binding, pwd: "123456", processors: make(map[string]chan *Connection)}
}

func (s *Server) Start() error {
	store, err := storage.OpenStore("")
	if err != nil {
		return err
	}
	s.store = store

	addr, err := net.ResolveTCPAddr("tcp", s.Binding)
	if err != nil {
		return err
	}
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return err
	}
	s.listener = listener

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.listener.Close()
			s.listener = nil
			return err
		}
		s.processConnection(conn)
	}

	return nil
}

func (s *Server) Stop(f func()) {
	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
	if f != nil {
		f()
	}
}

func (s *Server) processConnection(conn net.Conn) {
	buf := bufio.NewReader(conn)

	// The first line sent upon connection must be:
	//
	// AHOY pwd:<password> other:pair more:etc\n
	line, err := buf.ReadString('\n')
	if err != nil {
		fmt.Println("Closing connection: ", err)
		conn.Close()
		return
	}

	valid := strings.HasPrefix(line, "AHOY ")
	if !valid {
		fmt.Println("Invalid preamble", line)
		conn.Close()
		return
	}

	pairs := strings.Split(line, " ")
	var attrs = make(map[string]string, len(pairs)-1)

	for _, pair := range pairs[1:] {
		two := strings.Split(pair, ":")
		if len(two) != 2 {
			fmt.Println("Invalid pair", pair)
			conn.Close()
			return
		}

		key := strings.ToLower(two[0])
		value := two[1]
		attrs[key] = value
	}

	for key, value := range attrs {
		switch key {
		case "pwd":
			if value != s.pwd {
				fmt.Println("Invalid password")
				conn.Close()
				return
			}
			attrs["pwd"] = "<redacted>"
		}
	}

	conn.Write([]byte("OK\n"))

	id, ok := attrs["id"]
	if !ok {
		id = conn.RemoteAddr().String()
	}
	c := &Connection{
		ident: id,
		conn:  conn,
		buf:   buf,
	}
	go process(c, s)
}

func process(conn *Connection, server *Server) {
	for {
		cmd, e := conn.buf.ReadString('\n')
		if e != nil {
			fmt.Println(e)
			conn.Close()
			break
		}

		fmt.Println(cmd)

		switch {
		case cmd == "END\n":
			conn.Ok()
			conn.Close()
			break
		case strings.HasPrefix(cmd, "POP "):
			qs := strings.Split(cmd, " ")[1:]
			job := server.store.Pop(qs...)
			res, err := json.Marshal(job)
			if err != nil {
				conn.Error(err)
				break
			}
			conn.Result(res)
		case strings.HasPrefix(cmd, "PUSH {"):
			job, err := ParseJob([]byte(cmd[5:]))
			if err != nil {
				conn.Error(err)
				break
			}
			qname := job.Queue
			err = server.store.LookupQueue(qname).Push(job)
			if err != nil {
				conn.Error(err)
				break
			}

			conn.Result([]byte(job.Jid))
		default:
			conn.Error(errors.New("unknown command"))
		}
	}
}
