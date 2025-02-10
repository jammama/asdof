// 유저리스트 토글
const userListContainer = document.getElementById('userListContainer');
const userListBtn = document.getElementById('userListBtn');

userListBtn.onclick = () => {
    if (userListContainer.style.display === 'none') {
        userListContainer.style.display = 'block';
    } else {
        userListContainer.style.display = 'none';
    }
}