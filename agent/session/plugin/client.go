package client

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/aliyun/aliyun_assist_client/agent/log"
	"github.com/aliyun/aliyun_assist_client/agent/session/plugin/message"
	"github.com/containerd/console"
	"github.com/creack/goselect"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
)

const (
	committedSuicide = iota
	killed
)

type Client struct {
	Dialer                   *websocket.Dialer
	Conn                     *websocket.Conn
	URL                      string
	token                    string
	Connected                bool
	Output                   io.Writer
	Input 					 io.ReadCloser
	WriteMutex               *sync.Mutex
	EscapeKeys               []byte
	PortForward				 bool // true means the client is for portforward
	poison                   chan bool
	StreamDataSequenceNumber int64
	rawmode                  bool //true means not use console mode
	verbosemode              bool
	real_connected           bool
}

func NewClient(inputURL string, input io.ReadCloser, output io.Writer, portForward bool, token string,  rawmode bool, verbosemode bool) (*Client, error) {
	return &Client{
		Dialer:                   &websocket.Dialer{},
		URL:                      inputURL,
		token:                    token,
		WriteMutex:               &sync.Mutex{},
		Output:                   output, // os.Stdout
		Input: 					  input,  // os.Stdin
		PortForward: 			  portForward,		
		StreamDataSequenceNumber: 0,
		rawmode:                  rawmode,
		real_connected:           false,
		verbosemode:              verbosemode,
		poison:					  make(chan bool),
	}, nil
}

func (c *Client) write(data []byte) error {

	c.WriteMutex.Lock()
	defer c.WriteMutex.Unlock()
	return c.Conn.WriteMessage(websocket.BinaryMessage, data)
}

// Connect tries to dial a websocket server
func (c *Client) Connect() error {
	// Open WebSocket connection

	logrus.Debugln("Connecting to websocket: ", c.URL)
	header := http.Header{}
	header.Add("x-acs-session-token", c.token)
	conn, _, err := c.Dialer.Dial(c.URL, header)
	if err != nil {
		return err
	}
	c.Conn = conn
	c.Connected = true

	// Initialize message types for gotty
	// go c.pingLoop()

	return nil
}

func (c *Client) pingLoop() {
	for {
		if c.Connected {
			logrus.Debugf("Sending ping")
			err := c.Conn.WriteMessage(websocket.PingMessage, []byte("keepalive"))
			if err != nil {
				logrus.Warnf("c.write: %v", err)
			}
		}

		time.Sleep(30 * time.Second)
	}
}


func (c *Client) Loop() error {

	if !c.Connected {
		err := c.Connect()
		if err != nil {
			return err
		}
	}

	if !c.rawmode {
		if runtime.GOOS == "darwin" {
			stdin := int(os.Stdin.Fd())
			log.GetLogger().Infoln("under darwin")
			oldState, err := terminal.MakeRaw(stdin)
			if err != nil {
				log.GetLogger().Errorln(err)
				fmt.Printf("capture stdin failed %s\r\n", err)
			}
			defer func() {
				terminal.Restore(stdin, oldState)
				if !c.PortForward {
					os.Exit(1)
				}
			}()
		} else {
			term, err := console.ConsoleFromFile(os.Stdout)
			if err != nil {
				log.GetLogger().Errorln(err)
				return fmt.Errorf("os.Stdout is not a valid terminal")
			}
			err = term.SetRaw()
			if err != nil {
				return fmt.Errorf("Error setting raw terminal: %v", err)
			}
			defer func() {
				term.Reset()
				if !c.PortForward {
					os.Exit(1)
				}
			}()
		}

	} else {
		log.GetLogger().Infoln("under rawmode")
		defer func() {
			if !c.PortForward {
				os.Exit(1)
			}
		}()
	}

	wg := &sync.WaitGroup{}

	wg.Add(1)
	go c.termsizeLoop(wg)

	if !c.rawmode {
		wg.Add(1)
		go c.writeLoop(wg)

	} else {
		wg.Add(1)
		go c.writeLoopRawMode(wg)
	}

	wg.Add(1)
	go c.readLoop(wg)

	/* Wait for all of the above goroutines to finish */
	//wg.Wait()
	<-c.poison

	logrus.Debug("Client.Loop() exiting")

	return nil
}

type winsize struct {
	Rows    uint16 `json:"rows"`
	Columns uint16 `json:"cols"`
}

func (c *Client) termsizeLoop(wg *sync.WaitGroup) int {
	defer wg.Done()
	fname := "termsizeLoop"

	ch := make(chan os.Signal, 1)
	notifySignalSIGWINCH(ch)
	defer resetSignalSIGWINCH()

	for {
		if b, err := syscallTIOCGWINSZ(); err != nil {
			//	logrus.Warn(err)
		} else {
			if err = c.SendResizeDataMessage(b); err != nil {
				log.GetLogger().Warnf("ws.WriteMessage failed: %v", err)
			}
		}
		select {
		case <-c.poison:
			/* Somebody poisoned the well; die */
			return die(fname, c.poison)
		case <-ch:
		}
	}
}

func bytesToIntU(b []byte) (int, error) {
	if len(b) == 3 {
		b = append([]byte{0}, b...)
	}
	bytesBuffer := bytes.NewBuffer(b)
	switch len(b) {
	case 1:
		var tmp uint8
		err := binary.Read(bytesBuffer, binary.BigEndian, &tmp)
		return int(tmp), err
	case 2:
		var tmp uint16
		err := binary.Read(bytesBuffer, binary.BigEndian, &tmp)
		return int(tmp), err
	case 4:
		var tmp uint32
		err := binary.Read(bytesBuffer, binary.BigEndian, &tmp)
		return int(tmp), err
	default:
		return 0, fmt.Errorf("%s", "BytesToInt bytes lenth is invaild!")
	}
}

func (c *Client) ProcessStatusDataChannel(payload []byte) error {
	if c.verbosemode {
		log.GetLogger().Infoln("read status data: ", payload)
	}
	code, err := bytesToIntU(payload[0:1])
	if err == nil {
		if code == 2 { //建立连接失败
			log.GetLogger().Errorln("connect failed code 2")
			c.Output.Write(payload)
			return errors.New("Failed to connect. code 2")
		} else if code == 5 { //关闭连接
			log.GetLogger().Errorln("connect failed code 5")
			c.Output.Write([]byte("session closed"))
			return errors.New("Connection closed. code 5")
		} else if code == 3 {

		}
	}
	return nil
}

func (c *Client) readLoop(wg *sync.WaitGroup) int {
	defer wg.Done()
	fname := "readLoop"

	type MessageNonBlocking struct {
		Msg message.Message
		Err error
	}
	msgChan := make(chan MessageNonBlocking)

	for {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logrus.Debug("readLoop returned so msgChan closed", r)
				}
			}()
			_, data, err := c.Conn.ReadMessage()
			if c.verbosemode {
				log.GetLogger().Infoln("read msg: ", string(data))
			}
			streamDataMessage := message.Message{}
			if err == nil {
				if err = streamDataMessage.Deserialize(data); err != nil {
					log.GetLogger().Errorf("Cannot deserialize raw message, err: %v.", err)
				}
			} else {
				log.GetLogger().Errorln("read msg err")
				openPoison(fname, c.poison)
			}

			if c.verbosemode {
				log.GetLogger().Infoln("read msg num : ", streamDataMessage.SequenceNumber)
			}

			msgChan <- MessageNonBlocking{Msg: streamDataMessage, Err: err}
			// time.Sleep(time.Second * 1)
			// msgChan <- MessageNonBlocking{Data:  []byte("c"), Err: nil}
		}()

		select {
		case <-c.poison:
			close(msgChan)
			return die(fname, c.poison)
		case msg := <-msgChan:
			if msg.Err != nil {
				log.GetLogger().Errorln("read msg err", msg.Err)
				if _, ok := msg.Err.(*websocket.CloseError); !ok {
					log.GetLogger().Warnf("c.Conn.ReadMessage: %v", msg.Err)
				}

				return openPoison(fname, c.poison)
			}
			if msg.Msg.Validate() != nil {

				log.GetLogger().Errorln("An error has occured, msg is invalid")
				return openPoison(fname, c.poison)
			}

			switch msg.Msg.MessageType {
			case message.OutputStreamDataMessage: // data
				c.real_connected = true
				c.Output.Write(msg.Msg.Payload)
				break
			case message.StatusDataChannel: // data
				if c.ProcessStatusDataChannel(msg.Msg.Payload) != nil {
					return openPoison(fname, c.poison)
				}
				break
			default:
				// logrus.Warnf("Unhandled protocol message")
			}
		}
	}
	return 0
}

type exposeFd interface {
	Fd() uintptr
}

func (c *Client) writeLoopRawMode(wg *sync.WaitGroup) int {
	defer wg.Done()
	fname := "writeLoop"

	buff := make([]byte, 2048)
	br := bufio.NewReader(c.Input)
	if c.PortForward {
		// wait agent build local connect
		time.Sleep(time.Duration(2) * time.Second)
		c.real_connected = true
	}

	var resend_buff []byte
	for {
		time.Sleep(time.Duration(100) * time.Millisecond)
		size, err := br.Read(buff)
		if err != nil {
			log.GetLogger().Errorf("get raw input failed: %v", err)
			// tell agent to close session
			log.GetLogger().Infoln("local conn closed, send CloseMessage")
			if err = c.SendCloseMessage(); err != nil {
				log.GetLogger().Errorf("SendCloseMessage err: %v", err)
			}
			return openPoison(fname, c.poison)
		}
		if size == 0 {
			continue
		}

		data := buff[:size]

		if c.real_connected == true {
			if len(resend_buff) > 0 {
				time.Sleep(time.Duration(100) * time.Millisecond)
				c.SendStreamDataMessage(resend_buff)
				log.GetLogger().Infoln("agent ready resend user input:", string(resend_buff), len(resend_buff))
				resend_buff = nil
			}
			err = c.SendStreamDataMessage(data)
			if err != nil {
				return openPoison(fname, c.poison)
			}
			if c.verbosemode {
				log.GetLogger().Infoln("send user input:", string(data), size)
			}

		} else {
			if len(resend_buff) == 0 {
				resend_buff = make([]byte, size)
				copy(resend_buff, buff[:size])
				log.GetLogger().Infoln("store user input:", string(data), size)
			}

		}

	}

	return 0
}

func (c *Client) writeLoop(wg *sync.WaitGroup) int {
	defer wg.Done()
	fname := "writeLoop"

	buff := make([]byte, 2048)

	rdfs := &goselect.FDSet{}
	reader := io.ReadCloser(c.Input)

	pr := NewEscapeProxy(reader, c.EscapeKeys)
	defer reader.Close()

	for {
		select {
		case <-c.poison:
			return die(fname, c.poison)
		default:
		}

		rdfs.Zero()
		rdfs.Set(reader.(exposeFd).Fd())
		err := goselect.Select(1, rdfs, nil, nil, 50*time.Millisecond)
		if err != nil {
			// log.GetLogger().Errorf("get raw input failed: %v", err)
			continue
			// return openPoison(fname, c.poison)
		}
		if rdfs.IsSet(reader.(exposeFd).Fd()) {
			size, err := pr.Read(buff)

			if err != nil {
				log.GetLogger().Infoln("err in input empty")
				if err == io.EOF {
					log.GetLogger().Infoln("EOF in input empty")
					// Send EOF to GoTTY

					// Send 'Input' marker, as defined in GoTTY::client_context.go,
					// followed by EOT (a translation of Ctrl-D for terminals)
					err = c.SendStreamDataMessage((append([]byte{}, byte(4))))

					return openPoison(fname, c.poison)

					continue
				} else {
					log.GetLogger().Errorln("err in input empty", err)
					return openPoison(fname, c.poison)
				}
			}

			if size <= 0 {
				log.GetLogger().Infoln("user input empty")
				continue
			}

			data := buff[:size]
			if c.verbosemode {
				log.GetLogger().Infoln("begin send user input:", string(data), size)
			}
			err = c.SendStreamDataMessage(data)
			if err != nil {
				return openPoison(fname, c.poison)
			}

		}
	}
	return 0
}

func (c *Client) SendStreamDataMessage(inputData []byte) (err error) {
	if len(inputData) == 0 {
		log.GetLogger().Debugf("Ignoring empty stream data payload.")
		return nil
	}

	agentMessage := &message.Message{
		MessageType:    message.InputStreamDataMessage,
		SchemaVersion:  "1.01",
		CreatedDate:    uint64(time.Now().UnixNano() / 1000000),
		SequenceNumber: c.StreamDataSequenceNumber,
		PayloadLength:  uint32(len(inputData)),
		Payload:        inputData,
	}

	if c.verbosemode {
		log.GetLogger().Infoln("SendStreamDataMessage num: ", c.StreamDataSequenceNumber)
	}

	msg, err := agentMessage.Serialize()
	if err != nil {
		return fmt.Errorf("cannot serialize StreamData message %v", agentMessage)
	}

	if err = c.sendMessage(msg, websocket.BinaryMessage); err != nil {
		log.GetLogger().Errorf("Error sending stream data message %v", err)
		log.GetLogger().Infoln("disconnect, plugin exit")
		// os.Exit(1)
		c.Connected = false
		return err
	}

	if c.verbosemode {
		log.GetLogger().Println("SendStreamDataMessage:", msg)
	}

	c.StreamDataSequenceNumber = c.StreamDataSequenceNumber + 1
	return nil
}

func (c *Client) SendCloseMessage() (err error) {
	inputData := []byte("1")
	agentMessage := &message.Message{
		MessageType:    message.CloseDataChannel,
		SchemaVersion:  "1.01",
		CreatedDate:    uint64(time.Now().UnixNano() / 1000000),
		SequenceNumber: c.StreamDataSequenceNumber,
		PayloadLength:  uint32(len(inputData)),
		Payload:        inputData,
	}

	if c.verbosemode {
		log.GetLogger().Infoln("SendCloseMessage num: ", c.StreamDataSequenceNumber)
	}

	msg, err := agentMessage.Serialize()
	if err != nil {
		return fmt.Errorf("cannot serialize StreamData message %v", agentMessage)
	}

	if err = c.sendMessage(msg, websocket.BinaryMessage); err != nil {
		log.GetLogger().Errorf("Error sending stream data message %v", err)
		log.GetLogger().Infoln("disconnect, plugin exit")
		// os.Exit(1)
		c.Connected = false
		return err
	}
	log.GetLogger().Infoln("SendCloseMessage")

	c.StreamDataSequenceNumber = c.StreamDataSequenceNumber + 1
	return nil
}

func (c *Client) SendResizeDataMessage(inputData []byte) (err error) {
	if len(inputData) == 0 {
		log.GetLogger().Debugf("Ignoring empty stream data payload.")
		return nil
	}

	agentMessage := &message.Message{
		MessageType:    message.SetSizeDataMessage,
		SchemaVersion:  "1.01",
		CreatedDate:    uint64(time.Now().UnixNano() / 1000000),
		SequenceNumber: c.StreamDataSequenceNumber,
		PayloadLength:  uint32(len(inputData)),
		Payload:        inputData,
	}
	msg, err := agentMessage.Serialize()
	if err != nil {
		log.GetLogger().Errorf("cannot serialize StreamData message %v", agentMessage)
		return fmt.Errorf("cannot serialize StreamData message %v", agentMessage)
	}

	if err = c.sendMessage(msg, websocket.BinaryMessage); err != nil {
		log.GetLogger().Errorf("Error sending stream data message %v", err)
		return err
	}

	c.StreamDataSequenceNumber = c.StreamDataSequenceNumber + 1
	return nil
}

func (c *Client) sendMessage(input []byte, inputType int) error {
	defer func() {
		if msg := recover(); msg != nil {
			log.GetLogger().Errorf("WebsocketChannel  run panic: %v", msg)
			log.GetLogger().Errorf("%s: %s", msg, debug.Stack())
		}
	}()

	if len(input) < 1 {
		log.GetLogger().Errorln("Can't send message: Empty input.")
		return errors.New("Can't send message: Empty input.")
	}

	c.WriteMutex.Lock()
	err := c.Conn.WriteMessage(inputType, input)
	if c.verbosemode {
		log.GetLogger().Infoln("begin send msg: ", string(input))
	}
	if err != nil {
		log.GetLogger().Errorf("send messagefaile, %v", err)
	}
	c.WriteMutex.Unlock()
	return err
}

func openPoison(fname string, poison chan bool) int {
	logrus.Debug(fname + " suicide")

	/*
	 * The close() may raise panic if multiple goroutines commit suicide at the
	 * same time. Prevent that panic from bubbling up.
	 */
	defer func() {
		if r := recover(); r != nil {
			logrus.Debug("Prevented panic() of simultaneous suicides", r)
		}
	}()

	/* Signal others to die */
	close(poison)

	return committedSuicide
}

func die(fname string, poison chan bool) int {
	logrus.Debug(fname + " died")

	wasOpen := <-poison
	if wasOpen {
		logrus.Error("ERROR: The channel was open when it wasn't supposed to be")
	}

	return killed
}
