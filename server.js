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
    const userId = parseId(socket.id);
    io.to(socket.id).emit('id', userId);
    const url = new URL(referer);
    let roomName = url.pathname.split('/')[1] || 'about';// 방 이름 추출
    if(roomName === 'about' && ['www.asdof.xyz'|| 'asdof.xyz'].indexOf(url.hostname) === -1){
        roomName = url.hostname.split('.')[0];
    }
    socket.join(roomName);

    if (roomName === 'about') {
        io.to(socket.id).emit('message', {id:'ADMIN', msg:
                'This is about page. ' +
                'You can start your chat here, or join other room by typing your own room like: asdof.xyx/roomname.' +
                '\n Asdof is simple, do nothing to your message. ' +
                'Everything will reset after you leave this page. ' +
                'No log, No auth, No record. So feel free and be aware of your secure privacy. '
        });
    } else {
        io.to(socket.id).emit('message', {id:'ADMIN', msg: 'Welcome to ' + roomName + ' room.'});
    }

    socket.on('message', (msg) => {
        io.to(roomName).emit('message', {id : userId, msg, time: Date.now()}); // 같은 방에 있는 모든 클라이언트에 메시지 브로드캐스트
    });

    socket.on('disconnect', () => {
        io.to(roomName).emit('message', {id:'ADMIN', msg: userId + ' has left.'});
    });
});

// 서버 실행
server.listen(3000, () => {
    console.log('Server is running on http://localhost:3000');
});

// socket id를 user Id화
function parseId(key) {
    const chars = key.split('').map(char => char.charCodeAt(0));
    const sum = chars.reduce((p, c) => p + c, 0);
    const baseId = Array(6).fill(sum)
    chars.forEach((char, i) => baseId[i % 6] += char);
    return baseId.slice(0,2).map(r => String.fromCharCode(r % 26 + 65)).join('') +
        baseId.slice(2,4).map(r => (r % 10)).join('') +
        String.fromCodePoint(baseId[5] % 847 + 0x1F300);
}