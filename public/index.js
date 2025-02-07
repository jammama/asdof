const chatBox = document.getElementById('chatBox');
const messageInput = document.getElementById('messageInput');
const sendMessageBtn = document.getElementById('sendMessageBtn');
let myId = '';
// userName

const socket = io();
// 접속시 parsing된 본인 id 확인
socket.on('id', (data) => {myId = data})

// 메시지 수신
socket.on('message', (data) => {
    displayMessage(data);
    scrollToBottom();
});
// 메세지 전송
function sendMessage() {
    const messageText = messageInput.value;
    if(messageText === '') return;
    messageInput.value = '';
    const res = socket.emit('message', messageText)
    if(!res){
        messageInput.value = messageText;
    }
}

// 메세지 노출
function displayMessage(data) {
    const messageDiv = document.createElement('div');
    const userType = myId === data.id ? 'user1' : 'user2';
    messageDiv.classList.add('message', userType);

    const userId = document.createElement('div');
    userId.classList.add('message-id');
    userId.textContent = data.id;

    const messageContent = document.createElement('div');
    messageContent.classList.add('message-text');
    messageContent.textContent = data.msg;

    messageDiv.appendChild(userId);
    messageDiv.appendChild(messageContent);
    chatBox.appendChild(messageDiv);
}

// Scroll to the bottom
function scrollToBottom() {
    chatBox.scrollTop = chatBox.scrollHeight;
}

sendMessageBtn.addEventListener('click', sendMessage);

// Send message on Enter key
messageInput.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.shiftKey) {
        event.preventDefault();
        sendMessage();
    }
});