//Create new peer connection
let pc = new RTCPeerConnection({
    iceServers: [
        {
            urls: 'turn:127.0.0.1:3478',
            username: 'admin',
            credential: 'admin'
        }
    ]
})

//Create datachanel on peer conection
let sendChannel = pc.createDataChannel('')
sendChannel.onclose = () => console.log('sendChannel has closed')
sendChannel.onopen = () => {
    console.log('sendChannel has opened')
    sendChannel.send("HELLO FROM WEB")
}
sendChannel.onmessage = e => console.log(e.data)

//pc.oniceconnectionstatechange = e => log(pc.iceConnectionState)
//When local iceCandidates created we send it to our server
//We not recieves iceCandidates from server (but in real world must) 
//and in result we have only 1 candidate as TURN server on both sides (connection 100% relayed)
pc.onicecandidate = event => {
    fetch('/web_candidate', {
        method: 'POST',
        body: JSON.stringify(event.candidate.candidate)
    })
}

pc.onnegotiationneeded = e =>
    pc.createOffer().then(sdp => { //send offer to server
        pc.setLocalDescription(sdp) //set local description
        fetch('/websdp', {
            method: 'POST',
            body: JSON.stringify(sdp)
        }).then(res => res.json())
            .then(data => {
                pc.setRemoteDescription(data) //set remote description
                
                
            })
    })