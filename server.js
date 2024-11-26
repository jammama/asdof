const express = require('express');
const http = require('http');
const { Server } = require('socket.io');
const path = require('path');

const app = express();
const server = http.createServer(app);
const io = new Server(server);

// 정적 파일 제공
app.use(express.static(path.join(__dirname, 'public')));

// 모든 경로에서 index.html 제공
app.get('*', (req, res) => {
    res.sendFile(path.join(__dirname, 'public', 'index.html'));
});

// 소켓 연결
io.on('connection', (socket) => {
    const referer = socket.handshake.headers.referer; // 클라이언트 URL
    console.log(socket.handshake.headers)
    const roomName = new URL(referer).pathname.split('/')[1]; // 방 이름 추출

    if (roomName) {
        console.log(`User joined room: ${roomName}`);
        socket.join(roomName);
    }else {
        socket.join('about');
        console.log('User joined room: about');
        io.to('about').emit('info', {id:'admin', msg:
                'This is about page. ' +
                'You can start your chat here, or join other room by typing your own room like : asdof.xyx/roomname' +
                'Asdof is simple. Do nothing to your message. ' +
                'Everything will reset if you leave this page.' +
                'No log, No auth, No record. So feel free but always be aware of your secure privacy.'
        })
        ;
    }

    socket.on('message', (msg) => {
        console.log(`Message from ${roomName}: ${socket.id}`);
        io.to(roomName).emit('message', {id : socket.id, msg}); // 같은 방에 있는 모든 클라이언트에 메시지 브로드캐스트
    });

    socket.on('disconnect', () => {
        console.log('User disconnected');
    });
});

// 서버 실행
server.listen(3000, '0.0.0.0', () => {
    console.log('Server is running on http://localhost:3000');
});