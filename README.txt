ATTEMPT TO EXPLAIN:

Part 1: communicate webrtc client (answer/server) -> turn <- webrtc client (offer/client)

0: Use pion/turn as  STUN/TURN server.

Run it from "turn" folder via command: go run turn.go -public-ip 127.0.0.1 -users admin=admin
All TURN servers operate like STUN first (if direct connect can be established betwenn client) and TURN last (if no way without relay)

1.1: We need exchange SDP offer from client to server and from server to client
NOTE: we used 2 rest endpoints - one in server and one on client, we know serverIP and clientIP on start (in real world we use any kind of signaling server with any transport)
Sever:
    1.1.1 create webrtc.NewPeerConnection()
    1.1.2 wait for SDP offer on rest endpoint 127.0.0.1:60000/sdp
    1.1.3 peerConnection.SetRemoteDescription(sdp)
    1.1.4 peerConnection.CreateAnswer(nil)  //creating SDP info of our server peerConnection
    1.1.5 send answer to client rest endpoint on 127.0.0.1:50000
    1.1.6 set created answer (localDescription) with peerConnection.SetLocalDescription(answer)
Client:
    1.1.1 peerConnection.CreateOffer(nil)
    1.1.2 peerConnection.SetLocalDescription(offer)
    1.1.3 send offer to server rest endpoimt 127.0.0.1:60000/sdp
    1.1.4 set remote SDP from response peerConnection.SetRemoteDescription(sdp)

NOTE about ICE: Each side gathers all the addresses they want to use (all the possible addresses they are reachable at),
exchanges them, and then attempts to connect.
__IMPORTANT__ NOTE 2: A Relay Candidate is generated by using a TURN Server 
(turn server add line with 'relay' type, if this choosen later, than relayed connection will be created)

1-2: We need to exchange iceCandidates
On each callback of peerConnection.OnICECandidate - we collect ICE candidates in pendingCandidates map (rule for both sides)
Server:
    -After step 1.1.6 sends collect iceCandidates to client endpoint 127.0.0.1:50000/candidates
    -Wait for incomming on 127.0.0.1:60000/candidates
Client:
    -Do the same as server (different only in endpoints for exchange)

On this moment we complete exchange of INIT info


__IMPORTANT__ How to use relay mode force?
We need to use:
    ICETransportPolicy: webrtc.ICETransportPolicyRelay
When it set, then clients take only 'relay' candidates from TURN server (this line with ip address of TURN listener will be added by TURN server)

PART 2: add web client in chain

Server serve simple page on 127.0.0.1:6000/web (look in console)