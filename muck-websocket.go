package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

// 	"bytes"
//     	"golang.org/x/text/encoding/simplifiedchinese"
// 	"golang.org/x/text/transform"
//     	"io/ioutil"
	"github.com/axgle/mahonia"
	
	"github.com/Cristofori/kmud/telnet"
	"github.com/gorilla/websocket"
)

// func GbkToUtf8(s []byte) ([]byte, error) {
//     reader := transform.NewReader(bytes.NewReader(s), simplifiedchinese.GBK.NewDecoder())
//     d, e := ioutil.ReadAll(reader)
//     if e != nil {
//         return nil, e
//     }
//     return d, nil
// }

// func Utf8ToGbk(s []byte) ([]byte, error) {
//     reader := transform.NewReader(bytes.NewReader(s), simplifiedchinese.GBK.NewEncoder())
//     d, e := ioutil.ReadAll(reader)
//     if e != nil {
//         return nil, e
//     }
//     return d, nil
// }

var enc mahonia.Encoder = mahonia.NewEncoder("gbk")
var dec mahonia.Decoder = mahonia.NewDecoder("gbk")

func GbkToUtf8(s []byte) ([]byte, error) {
	res := enc.ConvertString(string(s))
	bytes := byte[](res)
	return bytes, nil
}

func Utf8ToGbk(s []byte) ([]byte, error) {
	res := dec.ConvertString(string(s))
	bytes := byte[](res)
	return bytes, nil
}

const useWss = true

// Flags
var addr = flag.String("addr", ":8181", "http service address")
var muckHost = flag.String("muck", "localhost:6661",
	"host and port for proxied muck")
var useTLS = flag.Bool("muck-ssl", false,
	"whether to connect to the muck with SSL.")

// Telnet commands
const FORWARDED = 113 // The new telnet option constant.
var willForwardCmd = telnet.BuildCommand(telnet.WILL, FORWARDED)
var beginForwardCmd = telnet.BuildCommand(telnet.SB, FORWARDED)
var endForwardCmd = telnet.BuildCommand(telnet.SE)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func openTelnet() (t *telnet.Telnet, err error) {
	var conn net.Conn

	if *useTLS {
		conn, err = tls.Dial("tcp", *muckHost, &tls.Config{
			InsecureSkipVerify: true,
		})
	} else {
		conn, err = net.Dial("tcp", *muckHost)
	}
	if err != nil {
		return nil, err
	}

	return telnet.NewTelnet(conn), nil
}

func telnetProxy(w http.ResponseWriter, r *http.Request) {
	log.Printf("telnetProxy %s", r.URL.Path);
	if r.URL.Path != "/" {
		http.Error(w, "Not found", 404)
		return
	}
	if r.Method != "GET" {
		log.Printf("telnetProxy not GET");
		http.Error(w, "Method not allowed", 405)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "Error creating websocket", 500)
		log.Print("upgrade:", err)
		return
	}
	defer c.Close()

	log.Printf("Opening a proxy for '%s'", r.RemoteAddr)
	t, err := openTelnet()
	if err != nil {
		log.Println("Error opening telnet proxy: ", err)
		return
	}
	defer t.Close()

	// Send over codes containing the user's real ip.
	// 1. Indicate our intention.
	t.SendCommand(telnet.WILL)
	t.Write([]byte{FORWARDED})
	// TODO: Use a listener function to confirm whether or not the server supports forwarding.
	// 2. Negotiate the start of the suboption transmission.
	t.SendCommand(telnet.SB)
	t.Write([]byte{FORWARDED})
	// 3. Send our new hostname.
	t.Write([]byte(strings.Split(r.RemoteAddr, ":")[0]))
	// 4. Indicate that we are done sending.
	t.SendCommand(telnet.SE)
	log.Printf("Connection open for '%s'. Proxying.", r.RemoteAddr)

	var wg sync.WaitGroup
	var once sync.Once
	wg.Add(1) // Exit when either goroutine stops.

	// Send messages from the websocket to the MUCK.
	go func() {
		defer once.Do(func() { wg.Done() })
		for {
			_, bytes, err := c.ReadMessage()
			if err != nil {
				log.Printf("Error reading from ws(%s): %v", r.RemoteAddr, err)
				break
			}
			if _, err := t.Write(bytes); err != nil {
				log.Printf("Error sending message to Muck for %s: %v",
					r.RemoteAddr, err)
				break
			}
// 			if rbytes, err := Utf8ToGbk(bytes); err == nil {
// 				// TODO: Partial writes.
// 				if _, err := t.Write(rbytes); err != nil {
// 					log.Printf("Error sending message to Muck for %s: %v",
// 						r.RemoteAddr, err)
// 					break
// 				}
// 			} else {
// 				log.Printf("Error Utf8ToGbk: %v", err)
// 			}
		}
	}()

	// Send messages from the MUCK to the websocket.
	go func() {
		defer once.Do(func() { wg.Done() })
		for {
			bytes := make([]byte, 10240)
			if _, err := t.Read(bytes); err != nil {
				log.Printf("Error reading from muck for %s: %v",
					r.Host, err)
				break
			}
			if err := c.WriteMessage(websocket.TextMessage, bytes); err != nil {
				log.Printf("Error sending to ws(%s): %v", r.RemoteAddr, err)
				break
			}
// 			if rbytes, err := GbkToUtf8(bytes); err == nil {
// 				if err := c.WriteMessage(websocket.TextMessage, rbytes); err != nil {
// 					log.Printf("Error sending to ws(%s): %v", r.RemoteAddr, err)
// 					break
// 				}
// 			} else {
// 				log.Printf("Error GbkToUtf8: %v", err)
// 			}
		}
	}()

	// Wait until either go routine exits and then close both connections.
	wg.Wait()
	log.Printf("Proxying completed for %s", r.RemoteAddr)
}

func main() {
	flag.Parse()
	log.SetFlags(0)
	log.Printf("starting...");

	http.HandleFunc("/", telnetProxy)
	if !useWss {
		log.Printf("ListenAndServe:%s", *addr);
		err := http.ListenAndServe(*addr, nil)
		if err != nil {
			log.Fatal("ListenAndServe: ", err)
		}
	} else {
		// Use this instead if you want to do SSL. You'll need to use `openssl`
		// to generate "cert.pem" and "key.pem" files.
		log.Printf("ListenAndServeTLS:%s", *addr);
		err := http.ListenAndServeTLS(*addr, "conf/cert.pem", "conf/key.pem", nil)
		if err != nil {
			log.Fatal("ListenAndServe: ", err)
		}
	}
		
}
