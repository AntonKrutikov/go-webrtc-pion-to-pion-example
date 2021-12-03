package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/pion/stun"
	"github.com/pion/turn/v2"
	"github.com/pion/webrtc/v3"
)

func signalCandidate(addr string, c *webrtc.ICECandidate) error {
	payload := []byte(c.ToJSON().Candidate)
	resp, err := http.Post(fmt.Sprintf("http://%s/candidate", addr), // nolint:noctx
		"application/json; charset=utf-8", bytes.NewReader(payload))
	if err != nil {
		fmt.Println(err)
		return err
	}

	if closeErr := resp.Body.Close(); closeErr != nil {
		return closeErr
	}

	return nil
}

func randomString(length int) string {
	rand.Seed(time.Now().UnixNano())
	b := make([]byte, length)
	rand.Read(b)
	return fmt.Sprintf("%x", b)[:length]
}

// This proxy used in embedded TURN server to operate on incoming and outcoming packets
// You can send captured bytes via channel but it doesn't make sense...
// Because TURN operate as udp proxy I think and data in webrtc encrypted
// You can see data flow, but have no access to actual data

// For example let collect decrypted STUN messages and pass it to chan
// Look at channel consume at the end of file

var turnInfoChan = make(chan *stun.Message)

type stunLoggerProxy struct {
	net.PacketConn
}

func (s *stunLoggerProxy) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	if n, err = s.PacketConn.WriteTo(p, addr); err == nil && stun.IsMessage(p) {
		msg := &stun.Message{Raw: p}
		if err = msg.Decode(); err != nil {
			return
		}

		turnInfoChan <- msg //fmt.Sprintf("Outbound STUN: %s \n", msg.String())
	}
	return
}

func (s *stunLoggerProxy) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	if n, addr, err = s.PacketConn.ReadFrom(p); err == nil && stun.IsMessage(p) {
		msg := &stun.Message{Raw: p}
		if err = msg.Decode(); err != nil {
			return
		}

		turnInfoChan <- msg //fmt.Sprintf("Inbound STUN: %s \n", msg.String())

	}

	return
}

//END of TURN helpers

func main() { // nolint:gocognit
	offerAddr := flag.String("offer-address", "localhost:50000", "Address that the Offer HTTP server is hosted on.")
	answerAddr := flag.String("answer-address", ":60000", "Address that the Answer HTTP server is hosted on.")
	flag.Parse()

	var candidatesMux sync.Mutex

	// Prepare the configuration (same for both)
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs:           []string{"turn:127.0.0.1:3478"},
				Username:       "admin",
				CredentialType: webrtc.ICECredentialTypePassword,
				Credential:     "admin",
			},
		},
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	}

	//Our simple server can handle 1 pion client and 1 web client
	//Because it initial exchange little differ next you can see some copipast
	//But keep in mind: we have 2 separate peerConnection objects

	//1:
	// For another pion webrtc client
	// Create a new RTCPeerConnection
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := peerConnection.Close(); err != nil {
			fmt.Printf("cannot close peerConnection: %v\n", err)
		}
	}()

	pendingCandidates := make([]*webrtc.ICECandidate, 0)

	// When an ICE candidate is available send to the other Pion instance
	// the other Pion instance will add this candidate by calling AddICECandidate
	peerConnection.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}

		fmt.Println(c.ToJSON())
		candidatesMux.Lock()
		defer candidatesMux.Unlock()

		desc := peerConnection.RemoteDescription()
		if desc == nil {
			pendingCandidates = append(pendingCandidates, c)
		} else if onICECandidateErr := signalCandidate(*offerAddr, c); onICECandidateErr != nil {
			panic(onICECandidateErr)
		}
	})

	pendingCandidatesWeb := make([]*webrtc.ICECandidate, 0)

	//A HTTP handler that processes a SessionDescription given to us from the other Pion process
	http.HandleFunc("/sdp", func(w http.ResponseWriter, r *http.Request) {
		// Create a new RTCPeerConnection
		if err != nil {
			panic(err)
		}
		sdp := webrtc.SessionDescription{}
		if err := json.NewDecoder(r.Body).Decode(&sdp); err != nil {
			panic(err)
		}

		if err := peerConnection.SetRemoteDescription(sdp); err != nil {
			panic(err)
		}

		// Create an answer to send to the other
		answer, err := peerConnection.CreateAnswer(nil)
		if err != nil {
			panic(err)
		}

		// Send our answer to /sdp endpoint on offerer
		payload, err := json.Marshal(answer)
		if err != nil {
			panic(err)
		}
		resp, err := http.Post(fmt.Sprintf("http://%s/sdp", *offerAddr), "application/json; charset=utf-8", bytes.NewReader(payload))
		if err != nil {
			panic(err)
		} else if closeErr := resp.Body.Close(); closeErr != nil {
			panic(closeErr)
		}

		// Sets the LocalDescription, and starts our UDP listeners
		err = peerConnection.SetLocalDescription(answer)
		if err != nil {
			panic(err)
		}

		// candidatesMux.Lock()
		// for _, c := range pendingCandidates {
		// 	onICECandidateErr := signalCandidate(*offerAddr, c)
		// 	if onICECandidateErr != nil {
		// 		panic(onICECandidateErr)
		// 	}
		// }
		// candidatesMux.Unlock()
	})

	// A HTTP handler that allows the other Pion instance to send us ICE candidates
	// This allows us to add ICE candidates faster, we don't have to wait for STUN or TURN
	// candidates which may be slower
	http.HandleFunc("/candidate", func(w http.ResponseWriter, r *http.Request) {
		candidate, candidateErr := ioutil.ReadAll(r.Body)
		if candidateErr != nil {
			panic(candidateErr)
		}
		if candidateErr := peerConnection.AddICECandidate(webrtc.ICECandidateInit{Candidate: string(candidate)}); candidateErr != nil {
			panic(candidateErr)
		}
	})

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", s.String())

		if s == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure. It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that the PeerConnection may come back from PeerConnectionStateDisconnected.
			fmt.Println("Peer Connection has gone to failed exiting")
			os.Exit(0)
		}
	})

	// Register data channel creation handling
	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		fmt.Printf("New DataChannel %s %d\n", d.Label(), d.ID())

		// Register channel opening handling
		d.OnOpen(func() {
			fmt.Printf("Data channel '%s'-'%d' open. Random messages will now be sent to any connected DataChannels every 2 seconds\n", d.Label(), d.ID())

			for range time.NewTicker(2 * time.Second).C {
				message := randomString(15)
				fmt.Printf("Sending to PION WEBRTC Client'%s'\n", message)

				// Send the message as text
				sendTextErr := d.SendText(message)
				if sendTextErr != nil {
					panic(sendTextErr)
				}
			}
		})

		// Register text message handling
		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			fmt.Printf("Message from PION WEBRTC Client '%s': '%s'\n", d.Label(), string(msg.Data))
		})
	})

	//2: (for web client)
	/*
		1) Recieve offer on /web_sdp endpoint
		2) send answer as response
		3) Recieve iceCandidates on /web_candidates
		4) Do nothig and we have only turn relay candidate on both sides
	*/

	peerConnectionWeb, err := webrtc.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := peerConnection.Close(); err != nil {
			fmt.Printf("cannot close peerConnection: %v\n", err)
		}
	}()

	peerConnectionWeb.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}

		fmt.Println(c.ToJSON())
		candidatesMux.Lock()
		defer candidatesMux.Unlock()

		desc := peerConnectionWeb.RemoteDescription()
		if desc == nil {
			pendingCandidatesWeb = append(pendingCandidatesWeb, c)
		}
	})

	http.HandleFunc("/websdp", func(w http.ResponseWriter, r *http.Request) {

		sdp := webrtc.SessionDescription{}
		if err := json.NewDecoder(r.Body).Decode(&sdp); err != nil {
			panic(err)
		}

		if err := peerConnectionWeb.SetRemoteDescription(sdp); err != nil {
			panic(err)
		}

		// Create an answer to send to the other
		answer, err := peerConnectionWeb.CreateAnswer(nil)
		if err != nil {
			panic(err)
		}

		// Sets the LocalDescription, and starts our UDP listeners
		err = peerConnectionWeb.SetLocalDescription(answer)
		if err != nil {
			panic(err)
		}

		// Send our answer to /sdp endpoint on offerer
		payload, err := json.Marshal(answer)
		if err != nil {
			panic(err)
		}

		w.Write(payload)
	})

	http.HandleFunc("/web_candidate", func(w http.ResponseWriter, r *http.Request) {
		candidate, candidateErr := ioutil.ReadAll(r.Body)
		if candidateErr != nil {
			panic(candidateErr)
		}
		if candidateErr := peerConnectionWeb.AddICECandidate(webrtc.ICECandidateInit{Candidate: string(candidate)}); candidateErr != nil {
			panic(candidateErr)
		}
	})

	//same as for 1 client
	peerConnectionWeb.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("Web Peer Connection State has changed: %s\n", s.String())

		if s == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure. It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that the PeerConnection may come back from PeerConnectionStateDisconnected.
			fmt.Println("Web Peer Connection has gone to failed exiting")
			os.Exit(0)
		}

		if s == webrtc.PeerConnectionStateClosed {
			// Create a new RTCPeerConnection
			peerConnectionWeb, err = webrtc.NewPeerConnection(config)
			if err != nil {
				panic(err)
			}
		}
	})

	//same as for 1 client
	peerConnectionWeb.OnDataChannel(func(d *webrtc.DataChannel) {
		fmt.Printf("New DataChannel %s %d\n", d.Label(), d.ID())

		// Register channel opening handling
		d.OnOpen(func() {
			fmt.Printf("Data channel '%s'-'%d' open. Random messages will now be sent to any connected DataChannels every 2 seconds\n", d.Label(), d.ID())

			for range time.NewTicker(2 * time.Second).C {
				message := randomString(15)
				fmt.Printf("Sending to WEB'%s'\n", message)

				// Send the message as text
				sendTextErr := d.SendText(message)
				if sendTextErr != nil {
					panic(sendTextErr)
				}
			}
		})

		// Register text message handling
		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			fmt.Printf("Message from WEB '%s': '%s'\n", d.Label(), string(msg.Data))
		})
	})

	//Serve our page
	//Look in chrome console
	//NOTE: we handle only 1 client for simplicity (before update page - restart server pls)
	fs := http.FileServer(http.Dir("../web_client"))
	http.Handle("/web/", http.StripPrefix("/web/", fs))

	// For pion client Start HTTP server that accepts requests from the offer process to exchange SDP and Candidates
	go func() {
		panic(http.ListenAndServe(*answerAddr, nil))
	}()

	//Embed TURN server as gorutine

	go func() {
		port := 3478
		publicIP := "127.0.0.1"
		udpListener, err := net.ListenPacket("udp4", "0.0.0.0:"+strconv.Itoa(port))
		if err != nil {
			log.Panicf("Failed to create TURN server listener: %s", err)
		}

		realm := "pion.ly"
		usersMap := map[string][]byte{}
		usersMap["admin"] = turn.GenerateAuthKey("admin", realm, "admin")

		_, err = turn.NewServer(turn.ServerConfig{
			Realm: realm,
			// Set AuthHandler callback
			// This is called everytime a user tries to authenticate with the TURN server
			// Return the key for that user, or false when no user is found
			AuthHandler: func(username string, realm string, srcAddr net.Addr) ([]byte, bool) {
				if key, ok := usersMap[username]; ok {
					return key, true
				}
				fmt.Println("AUTH ERROR")
				return nil, false
			},
			// PacketConnConfigs is a list of UDP Listeners and the configuration around them
			PacketConnConfigs: []turn.PacketConnConfig{
				{
					PacketConn: &stunLoggerProxy{udpListener},
					RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
						RelayAddress: net.ParseIP(publicIP), // Claim that we are listening on IP passed by user (This should be your Public IP)
						Address:      "0.0.0.0",             // But actually be listening on every interface
					},
				},
			},
		})
	}()

	//Strange example to process TURN/STUN messages in another gorutine
	//In fact same behaviour we can achieve via callbacks in: (s *stunLoggerProxy) WriteTo and (s *stunLoggerProxy) ReadFrom
	go func() {
		for {
			m := <-turnInfoChan
			fmt.Printf("STUN: %s\n", m.Type)
		}
	}()

	select {}
}
